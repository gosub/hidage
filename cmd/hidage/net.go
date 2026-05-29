package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"hidage/internal/accounting"
	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/netstore"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/reputation"
	"hidage/internal/store"
	"hidage/internal/trust"
)

// acceptablePeer reports whether to push to a peer: never if blocked, always if
// explicitly trusted, otherwise unless its audit reputation is known-bad.
func acceptablePeer(policy *trust.Policy, rep *reputation.Store, pubHex string) bool {
	raw, err := hex.DecodeString(pubHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return false
	}
	switch policy.Verdict(ed25519.PublicKey(raw)) {
	case trust.Blocked:
		return false
	case trust.Trusted:
		return true
	default:
		return !rep.Bad(pubHex)
	}
}

// publishedStore is the node-wide store of objects this node serves to the network.
func publishedStore() (*store.KeyedStore, error) {
	return store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "published"))
}

func newServeCmd() *cobra.Command {
	var listen string
	var bootstrap []string
	var ratio float64
	var freeMiB int64
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a node: serve objects, accept pushes from trusted peers, join the DHT",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			pub, err := publishedStore()
			if err != nil {
				return err
			}
			node, err := p2p.New(kr.Identity(), pub, listen)
			if err != nil {
				return err
			}
			defer node.Close()

			ctx := context.Background()
			if err := node.EnableDHT(ctx); err != nil {
				return err
			}
			policy, err := trust.Load(paths.TrustPath)
			if err != nil {
				return err
			}
			ledger, err := accounting.Load(paths.AccountingPath)
			if err != nil {
				return err
			}
			free := freeMiB << 20
			node.EnablePush(pub,
				func(senderPub []byte) bool {
					if policy.Verdict(ed25519.PublicKey(senderPub)) == trust.Blocked {
						return false
					}
					return ledger.Allow(hex.EncodeToString(senderPub), 0, ratio, free)
				},
				func(senderPub []byte, b int64) {
					ledger.AddStoredForThem(hex.EncodeToString(senderPub), b)
				})
			if err := node.Bootstrap(ctx, bootstrap); err != nil {
				return err
			}
			go reprovide(ctx, node, pub)

			fmt.Println("hidage node listening; addresses:")
			for _, a := range node.Addrs() {
				fmt.Printf("  %s\n", a)
			}
			fmt.Println("Ctrl-C to stop.")
			waitForSignal()
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "listen multiaddr (default: ephemeral TCP on all interfaces)")
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s)")
	cmd.Flags().Float64Var(&ratio, "ratio", accounting.DefaultRatio, "max bytes stored per peer per byte they store for us")
	cmd.Flags().Int64Var(&freeMiB, "free", accounting.DefaultFreeQuota>>20, "free quota per peer, in MiB (no reciprocation required)")
	return cmd
}

func newPublishCmd() *cobra.Command {
	var bootstrap, peers []string
	var redundancy float64
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Erasure-code the latest snapshot, spread shards across peers, publish the head",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			folder := repo.Find(startDir())
			if folder == "" {
				return fmt.Errorf("not inside a hidage repository")
			}
			r, err := repo.Open(folder, kr.MasterKey(), config.DataStoreDir())
			if err != nil {
				return err
			}
			snap, err := r.Latest()
			if err != nil {
				return err
			}
			if snap == nil {
				return fmt.Errorf("no snapshots to publish")
			}
			objs, err := r.PublishSet(snap)
			if err != nil {
				return err
			}

			// Erasure-code the snapshot's objects into shards; the Release
			// descriptor (pushed to every peer) records how to reassemble them.
			rel, shardBlobs, err := pack.Build(objs, redundancy, snap.ID)
			if err != nil {
				return err
			}
			relBytes, err := json.Marshal(rel)
			if err != nil {
				return err
			}
			relID := store.ID(relBytes)

			node, err := p2p.New(kr.Identity(), nil, "")
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := node.EnableDHT(ctx); err != nil {
				return err
			}
			if err := node.Bootstrap(ctx, bootstrap); err != nil {
				return err
			}
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			policy, err := trust.Load(paths.TrustPath)
			if err != nil {
				return err
			}
			rep, err := reputation.Load(paths.ReputationPath)
			if err != nil {
				return err
			}
			ledger, err := accounting.Load(paths.AccountingPath)
			if err != nil {
				return err
			}

			// Filter peers by trust + reputation, then spread the shards across
			// the survivors so losing a minority of them still leaves K per pack.
			var accepted []peerID
			for _, addr := range peers {
				pid, err := node.Connect(ctx, addr)
				if err != nil {
					fmt.Printf("  %s: %v\n", addr, err)
					continue
				}
				pubHex, _ := p2p.PeerPubKeyHex(pid)
				if pubHex != "" && !acceptablePeer(policy, rep, pubHex) {
					fmt.Printf("  %s: skipped (blocked or low reputation)\n", pid)
					continue
				}
				accepted = append(accepted, peerID{id: pid, pubHex: pubHex})
			}
			if len(accepted) == 0 {
				return fmt.Errorf("no acceptable peers to publish to")
			}

			assignments := spreadShards(shardBlobs, relID, relBytes, len(accepted))
			for i, p := range accepted {
				push := assignments[i]
				var bytes int64
				for _, d := range push {
					bytes += int64(len(d))
				}
				stored, err := node.PushObjects(ctx, p.id, push)
				if err != nil {
					fmt.Printf("  %s: %v\n", p.id, err)
					continue
				}
				if stored == len(push) && p.pubHex != "" {
					ledger.AddTheyStoreForUs(p.pubHex, bytes)
				}
				fmt.Printf("  %s: stored %d shards (%d bytes)\n", p.id, stored, bytes)
			}
			if err := node.PutHead(ctx, kr.Identity(), relID); err != nil {
				return fmt.Errorf("publish head: %w", err)
			}
			fmt.Printf("Published snapshot %s as %d shards across %d peers (%.1fx redundancy); head updated.\n",
				snap.ID[:16], len(shardBlobs), len(accepted), redundancy)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s)")
	cmd.Flags().StringArrayVar(&peers, "peer", nil, "storage peer multiaddr(s) to push to")
	cmd.Flags().Float64Var(&redundancy, "redundancy", 2.0, "shards to emit per shard needed (e.g. 2.0 tolerates losing half)")
	return cmd
}

type peerID struct {
	id     peer.ID
	pubHex string
}

// spreadShards assigns shard blobs round-robin across n peers, and gives the
// Release descriptor to every peer (so it can always be fetched).
func spreadShards(shardBlobs map[string][]byte, relID string, relBytes []byte, n int) []map[string][]byte {
	out := make([]map[string][]byte, n)
	for i := range out {
		out[i] = map[string][]byte{relID: relBytes}
	}
	i := 0
	for id, data := range shardBlobs {
		out[i%n][id] = data
		i++
	}
	return out
}

func newNetRestoreCmd() *cobra.Command {
	var bootstrap []string
	var ownerHex string
	cmd := &cobra.Command{
		Use:   "net-restore <out-dir>",
		Short: "Recover a backup from the network (head pointer → snapshot → objects)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			owner := kr.Identity().Public
			if ownerHex != "" {
				raw, err := hex.DecodeString(ownerHex)
				if err != nil || len(raw) != ed25519.PublicKeySize {
					return fmt.Errorf("invalid --owner public key")
				}
				owner = ed25519.PublicKey(raw)
			}

			node, err := p2p.New(kr.Identity(), nil, "")
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := node.EnableDHT(ctx); err != nil {
				return err
			}
			if err := node.Bootstrap(ctx, bootstrap); err != nil {
				return err
			}

			var relID string
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				if relID, _ = node.GetHead(ctx, owner); relID != "" {
					break
				}
				time.Sleep(time.Second)
			}
			if relID == "" {
				return fmt.Errorf("could not resolve a head pointer for that owner")
			}

			cache, err := store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "cache"))
			if err != nil {
				return err
			}
			ns := netstore.New(node, cache)

			// Fetch the Release, then gather K-of-M shards per pack from whoever
			// still has them and rebuild every object.
			relBytes, err := ns.Get(relID)
			if err != nil {
				return fmt.Errorf("fetch release: %w", err)
			}
			var rel pack.Release
			if err := json.Unmarshal(relBytes, &rel); err != nil {
				return err
			}
			objects, err := pack.Reassemble(&rel, ns.Get)
			if err != nil {
				return fmt.Errorf("reassemble: %w", err)
			}
			for id, data := range objects {
				if err := cache.Put(id, data); err != nil {
					return err
				}
			}
			snap, err := repo.ParseSnapshot(objects[rel.SnapshotID])
			if err != nil {
				return err
			}
			loader := dag.NewLoader(cache, cache, kr.MasterKey())
			if err := repo.RestoreWith(loader, snap, args[0]); err != nil {
				return err
			}
			fmt.Printf("Recovered snapshot %s into %s\n", snap.ID[:16], args[0])
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s)")
	cmd.Flags().StringVar(&ownerHex, "owner", "", "owner public key hex (default: your own)")
	return cmd
}

// reprovide announces every locally-held object to the DHT.
func reprovide(ctx context.Context, node *p2p.Node, blobs *store.KeyedStore) {
	ids, err := blobs.List()
	if err != nil {
		return
	}
	for _, id := range ids {
		_ = node.Provide(ctx, id)
	}
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
