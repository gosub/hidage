package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/netstore"
	"hidage/internal/p2p"
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
			node.EnablePush(pub, func(senderPub []byte) bool {
				return policy.Verdict(ed25519.PublicKey(senderPub)) != trust.Blocked
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
	return cmd
}

func newPublishCmd() *cobra.Command {
	var bootstrap, peers []string
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Push the latest snapshot's objects to storage peers and publish the head pointer",
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

			node, err := p2p.New(kr.Identity(), nil, "")
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
			for _, addr := range peers {
				pid, err := node.Connect(ctx, addr)
				if err != nil {
					fmt.Printf("  %s: %v\n", addr, err)
					continue
				}
				if pubHex, err := p2p.PeerPubKeyHex(pid); err == nil && !acceptablePeer(policy, rep, pubHex) {
					fmt.Printf("  %s: skipped (blocked or low reputation)\n", pid)
					continue
				}
				stored, err := node.PushObjects(ctx, pid, objs)
				if err != nil {
					fmt.Printf("  %s: %v\n", pid, err)
					continue
				}
				fmt.Printf("  %s: stored %d/%d objects\n", pid, stored, len(objs))
			}
			if err := node.PutHead(ctx, kr.Identity(), snap.ID); err != nil {
				return fmt.Errorf("publish head: %w", err)
			}
			fmt.Printf("Published snapshot %s (%d objects); head pointer updated.\n", snap.ID[:16], len(objs))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s)")
	cmd.Flags().StringArrayVar(&peers, "peer", nil, "storage peer multiaddr(s) to push to")
	return cmd
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

			var snapID string
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				if snapID, _ = node.GetHead(ctx, owner); snapID != "" {
					break
				}
				time.Sleep(time.Second)
			}
			if snapID == "" {
				return fmt.Errorf("could not resolve a head pointer for that owner")
			}

			cache, err := store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "cache"))
			if err != nil {
				return err
			}
			ns := netstore.New(node, cache)
			snapBytes, err := ns.Get(snapID)
			if err != nil {
				return fmt.Errorf("fetch snapshot: %w", err)
			}
			snap, err := repo.ParseSnapshot(snapBytes)
			if err != nil {
				return err
			}
			loader := dag.NewLoader(ns, ns, kr.MasterKey())
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
