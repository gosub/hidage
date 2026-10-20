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

	"hidage/internal/account"
	"hidage/internal/accounting"
	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/friends"
	"hidage/internal/netstore"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/store"
)

// publishedStore is the node-wide store of objects this node serves to the network.
func publishedStore() (*store.KeyedStore, error) {
	return store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "published"))
}

// headsStore holds signed head records this node re-publishes on owners' behalf,
// keyed by owner public-key hex, so discovery survives an owner going offline.
func headsStore() (*store.KeyedStore, error) {
	return store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "heads"))
}

func newServeCmd() *cobra.Command {
	var listen string
	var bootstrap []string
	var ratio float64
	var freeMiB int64
	var leaseHours int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a node: serve objects, accept pushes from friends, join the DHT",
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
			settings, _ := config.Load()
			if !cmd.Flags().Changed("listen") && len(settings.Listen) > 0 {
				listen = settings.Listen[0]
			}
			if !cmd.Flags().Changed("lease") {
				leaseHours = settings.Hosting.LeaseDays * 24
			}
			pub, err := publishedStore()
			if err != nil {
				return err
			}
			book, err := loadFriends()
			if err != nil {
				return err
			}
			// Friends only: refuse inbound connections from anyone else.
			node, err := p2p.New(kr.Identity(), pub, listen, p2p.WithInboundFilter(func(id peer.ID) bool {
				book.Refresh() // pick up `friend add/rm` made while serving
				return book.IsFriendPeer(id)
			}))
			if err != nil {
				return err
			}
			defer node.Close()

			ctx := context.Background()
			if err := node.EnableDHT(ctx); err != nil {
				return err
			}
			ledger, err := accounting.Load(paths.AccountingPath)
			if err != nil {
				return err
			}
			heads, err := headsStore()
			if err != nil {
				return err
			}
			node.EnableHeadStore(func(ownerHex string, val []byte) error {
				return heads.Put(ownerHex, val) // owner-signed; re-published so recovery survives owner loss
			})
			free := freeMiB << 20
			node.EnablePush(pub,
				func(senderPub []byte, cumulative int64) bool {
					if !book.IsFriend(hex.EncodeToString(senderPub)) {
						return false // friends only
					}
					// cumulative is what this stream would have stored so far; the
					// ledger checks it stays within ratio×reciprocation + free.
					return ledger.Allow(hex.EncodeToString(senderPub), cumulative, ratio, free)
				},
				func(senderPub []byte, b int64) {
					pubHex := hex.EncodeToString(senderPub)
					ledger.AddStoredForThem(pubHex, b)
					book.Update(pubHex, func(f *friends.Friend) error { f.LastSeen = time.Now().UTC(); return nil })
				})
			if err := joinNetwork(ctx, node, bootstrap); err != nil {
				return err
			}
			go reprovideLoop(ctx, node, pub)
			go republishHeadsLoop(ctx, node, heads)
			go gcLoop(ctx, pub, time.Duration(leaseHours)*time.Hour)

			if err := writeNodeState(node.ListenAddrs()); err != nil {
				return err
			}
			defer os.Remove(nodeStatePath())
			fmt.Println("hidage node listening; addresses:")
			for _, a := range node.Addrs() {
				fmt.Printf("  %s\n", a)
			}
			fmt.Println("Friends add you with the card from `hidage id --card`.")
			fmt.Println("Ctrl-C to stop.")
			waitForSignal()
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "listen multiaddr (default: first `listen` in config.toml)")
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s) (default: your friends)")
	cmd.Flags().Float64Var(&ratio, "ratio", accounting.DefaultRatio, "max bytes stored per peer per byte they store for us")
	cmd.Flags().Int64Var(&freeMiB, "free", accounting.DefaultFreeQuota>>20, "free quota per peer, in MiB (no reciprocation required)")
	cmd.Flags().IntVar(&leaseHours, "lease", 0, "hosted-shard lease in hours; data not renewed within this is reclaimed (default: hosting.lease_days)")
	return cmd
}

// gcLoop periodically reclaims hosted objects whose storage lease has lapsed
// (they were not renewed by a re-publish within the lease window).
func gcLoop(ctx context.Context, blobs *store.KeyedStore, lease time.Duration) {
	reap := func() {
		if n, err := blobs.Reap(lease); err == nil && n > 0 {
			fmt.Printf("[gc] reclaimed %d expired hosted objects\n", n)
		}
	}
	reap()
	interval := lease / 12
	if interval < time.Minute {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reap()
		}
	}
}

func newPublishCmd() *cobra.Command {
	var bootstrap, peers []string
	var redundancy float64
	var useFriends bool
	var minPeers int
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
			// Check where to publish before the expensive packing and networking.
			book, err := loadFriends()
			if err != nil {
				return err
			}
			if len(peers) == 0 {
				useFriends = true
				if len(book.List()) == 0 {
					return fmt.Errorf("no friends to publish to; add one with `hidage friend add <card>` (or pass --peer)")
				}
			}
			objs, err := r.PublishSet(snap)
			if err != nil {
				return err
			}
			settings, _ := config.Load()
			if !cmd.Flags().Changed("redundancy") {
				redundancy = settings.Backup.Redundancy
			}
			if !cmd.Flags().Changed("min-peers") {
				minPeers = settings.Backup.MinFriends
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
			if err := joinNetwork(ctx, node, bootstrap); err != nil {
				return err
			}
			node.WaitForPeers(ctx, 1, 15*time.Second) // settle the DHT before record ops

			// Merge this repo's release into the owner's account manifest, so the
			// single head pointer indexes every backed-up folder, not just this one.
			manifest := fetchManifest(ctx, node, kr.Identity().Public)
			manifest.Upsert(account.Entry{
				RepoID:    r.RepoID(),
				Name:      snap.Source,
				ReleaseID: relID,
				UpdatedAt: time.Now().UTC(),
			})
			manifestBytes, err := manifest.Bytes()
			if err != nil {
				return err
			}
			manifestID := store.ID(manifestBytes)
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			ledger, err := accounting.Load(paths.AccountingPath)
			if err != nil {
				return err
			}

			// Collect candidate peers: explicit --peer multiaddrs, plus (with
			// --friends) every friend reachable at a known address.
			seen := map[peer.ID]bool{}
			var accepted []peerID
			add := func(pid peer.ID) {
				if seen[pid] || pid == node.ID() {
					return
				}
				seen[pid] = true
				pubHex, _ := p2p.PeerPubKeyHex(pid)
				accepted = append(accepted, peerID{id: pid, pubHex: pubHex})
			}
			for _, addr := range peers {
				pid, err := node.Connect(ctx, addr)
				if err != nil {
					fmt.Printf("  %s: %v\n", addr, err)
					continue
				}
				add(pid)
			}
			if useFriends {
				for _, f := range book.List() {
					pid, err := f.PeerID()
					if err != nil {
						continue
					}
					if err := node.ConnectAddrs(ctx, pid, f.Addrs); err != nil {
						fmt.Printf("  %s: unreachable (%v)\n", f.Nickname, err)
						continue
					}
					add(pid)
				}
			}
			if len(accepted) == 0 {
				return fmt.Errorf("no friends reachable to publish to (see `hidage friend ls`)")
			}
			if len(accepted) < minPeers {
				return fmt.Errorf("only %d acceptable peer(s) reachable, need at least --min-peers=%d", len(accepted), minPeers)
			}
			// Erasure coding only helps if shards land on enough distinct peers.
			tolerate := int(float64(len(accepted)) * (1 - 1/redundancy))
			if tolerate == 0 {
				fmt.Printf("WARNING: %d peer(s) at %.1fx redundancy gives no fault tolerance "+
					"(losing one peer loses the backup); use more peers or higher --redundancy.\n",
					len(accepted), redundancy)
			} else {
				fmt.Printf("Spreading across %d peers; tolerates losing up to %d.\n", len(accepted), tolerate)
			}

			common := map[string][]byte{relID: relBytes, manifestID: manifestBytes}
			assignments := spreadShards(shardBlobs, common, len(accepted))
			dist := distribution{ReleaseID: relID, SnapshotID: snap.ID, Redundancy: redundancy, PublishedAt: time.Now().UTC(), Peers: map[string][]string{}}
			var holders []peerID
			for i, p := range accepted {
				push := assignments[i]
				var bytes int64
				var shardIDs []string
				for id, d := range push {
					bytes += int64(len(d))
					if _, isShard := shardBlobs[id]; isShard {
						shardIDs = append(shardIDs, id)
					}
				}
				stored, err := node.PushObjects(ctx, p.id, push)
				if err != nil {
					fmt.Printf("  %s: %v\n", p.id, err)
					continue
				}
				if stored < len(push) {
					fmt.Printf("  %s: stored only %d of %d objects (out of space?)\n", p.id, stored, len(push))
					continue
				}
				ledger.AddTheyStoreForUs(p.pubHex, bytes)
				dist.Peers[p.pubHex] = shardIDs // remember what to audit them for
				holders = append(holders, p)
				fmt.Printf("  %s: stored %d shards (%d bytes)\n", p.id, stored, bytes)
			}
			// Shards assigned to a peer that didn't store them are missing; the
			// release is only rebuildable if no more peers failed than it tolerates.
			if failed := len(accepted) - len(holders); failed > tolerate || len(holders) == 0 {
				return fmt.Errorf("only %d of %d peers stored their shards, too few to rebuild the backup; "+
					"the previous publish (if any) is still the one on record", len(holders), len(accepted))
			} else if failed > 0 {
				fmt.Printf("WARNING: %d peer(s) did not store their shards; the backup is rebuildable but has less fault tolerance than planned.\n", failed)
			}
			if err := writeDistribution(r.HidageFile("distribution.json"), dist); err != nil {
				return err
			}
			headVal, err := node.PutHead(ctx, kr.Identity(), manifestID)
			if err != nil {
				return fmt.Errorf("publish head: %w", err)
			}
			// Hand the signed head to peers so they keep it discoverable after we
			// go offline (they re-publish it; it's owner-signed, so safe to serve).
			headKey := p2p.HeadKey(kr.Identity().Public)
			for _, p := range holders {
				_ = node.PushHead(ctx, p.id, headKey, headVal)
			}
			fmt.Printf("Published %q (%s) as %d shards across %d peers (%.1fx redundancy).\n",
				snap.Source, r.RepoID()[:12], len(shardBlobs), len(holders), redundancy)
			fmt.Printf("Account now indexes %d backup(s).\n", len(manifest.Entries))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s) (default: your friends)")
	cmd.Flags().StringArrayVar(&peers, "peer", nil, "push to this peer multiaddr instead of your friends")
	cmd.Flags().BoolVar(&useFriends, "friends", false, "push to every reachable friend (the default when no --peer is given)")
	cmd.Flags().IntVar(&minPeers, "min-peers", 0, "fail unless at least this many acceptable peers are reachable (default: backup.min_friends)")
	cmd.Flags().Float64Var(&redundancy, "redundancy", 0, "shards to emit per shard needed, e.g. 2.0 tolerates losing half (default: backup.redundancy)")
	return cmd
}

type peerID struct {
	id     peer.ID
	pubHex string
}

// distribution records which shards of the latest release went to which peer, so
// audit can later challenge each peer for exactly the shards it agreed to hold.
// SnapshotID names the published snapshot so `status` can detect a repository
// whose latest snapshot has not been re-published.
type distribution struct {
	ReleaseID   string              `json:"releaseID"`
	SnapshotID  string              `json:"snapshotID"`
	Redundancy  float64             `json:"redundancy"`
	PublishedAt time.Time           `json:"publishedAt"`
	Peers       map[string][]string `json:"peers"` // peer pubkey hex -> shard ids
}

func writeDistribution(path string, d distribution) error {
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func readDistribution(path string) (distribution, error) {
	var d distribution
	data, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(data, &d)
}

// spreadShards assigns shard blobs round-robin across n peers, and gives the
// common objects (Release + account manifest) to every peer so they can always
// be fetched.
func spreadShards(shardBlobs, common map[string][]byte, n int) []map[string][]byte {
	out := make([]map[string][]byte, n)
	for i := range out {
		out[i] = map[string][]byte{}
		for id, data := range common {
			out[i][id] = data
		}
	}
	i := 0
	for id, data := range shardBlobs {
		out[i%n][id] = data
		i++
	}
	return out
}

// fetchManifest resolves the owner's current account manifest from the network,
// returning an empty one if none exists or it can't be verified.
func fetchManifest(ctx context.Context, node *p2p.Node, owner ed25519.PublicKey) *account.Manifest {
	mid, err := node.GetHead(ctx, owner)
	if err != nil || mid == "" {
		return account.New()
	}
	data, err := node.GetObject(ctx, mid)
	if err != nil || store.ID(data) != mid {
		return account.New()
	}
	m, err := account.Parse(data)
	if err != nil {
		return account.New()
	}
	return m
}

func newNetRestoreCmd() *cobra.Command {
	var bootstrap []string
	var ownerHex string
	var repoSel string
	var all bool
	cmd := &cobra.Command{
		Use:   "net-restore <out-dir>",
		Short: "Recover backups from the network (head → account manifest → shards)",
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
			if err := joinNetwork(ctx, node, bootstrap); err != nil {
				return err
			}

			var mid string
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				if mid, _ = node.GetHead(ctx, owner); mid != "" {
					break
				}
				time.Sleep(time.Second)
			}
			if mid == "" {
				return fmt.Errorf("could not resolve a head pointer for that owner")
			}

			cache, err := store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "cache"))
			if err != nil {
				return err
			}
			ns := netstore.New(node, cache)

			manBytes, err := ns.Get(mid)
			if err != nil {
				return fmt.Errorf("fetch account manifest: %w", err)
			}
			if store.ID(manBytes) != mid {
				return fmt.Errorf("account manifest failed integrity check")
			}
			manifest, err := account.Parse(manBytes)
			if err != nil {
				return err
			}
			if len(manifest.Entries) == 0 {
				return fmt.Errorf("owner has no published backups")
			}

			// Choose which backup(s) to restore.
			var targets []account.Entry
			switch {
			case all:
				targets = manifest.Entries
			case repoSel != "":
				e, err := manifest.Find(repoSel)
				if err != nil {
					return err
				}
				targets = []account.Entry{*e}
			case len(manifest.Entries) == 1:
				targets = manifest.Entries
			default:
				fmt.Println("Multiple backups found; pick one with --repo <name> or use --all:")
				for _, e := range manifest.Entries {
					fmt.Printf("  %-24s  %s\n", e.Name, e.RepoID[:12])
				}
				return fmt.Errorf("specify --repo or --all")
			}

			for _, e := range targets {
				if err := restoreRelease(ns, cache, kr.MasterKey(), e.ReleaseID, args[0]); err != nil {
					return fmt.Errorf("restore %q: %w", e.Name, err)
				}
				fmt.Printf("Recovered %q into %s\n", e.Name, args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s) (default: your friends)")
	cmd.Flags().StringVar(&ownerHex, "owner", "", "owner public key hex (default: your own)")
	cmd.Flags().StringVar(&repoSel, "repo", "", "which backup to restore (name or repo-id prefix)")
	cmd.Flags().BoolVar(&all, "all", false, "restore every backup in the account")
	return cmd
}

// restoreRelease fetches a Release, reassembles its objects from K-of-M shards,
// and restores the snapshot into outDir (under its source name).
func restoreRelease(ns *netstore.Store, cache *store.KeyedStore, masterKey []byte, relID, outDir string) error {
	relBytes, err := ns.Get(relID)
	if err != nil {
		return fmt.Errorf("fetch release: %w", err)
	}
	if store.ID(relBytes) != relID {
		return fmt.Errorf("release failed integrity check")
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
	return repo.RestoreWith(dag.NewLoader(cache, cache, masterKey), snap, outDir)
}

// reprovideLoop periodically announces every locally-held object to the DHT.
// Provider records expire (libp2p's default validity is ~48h), so a one-shot
// announce would make hosted shards undiscoverable — and unrecoverable — within
// a day; refreshing well inside that window keeps them findable.
func reprovideLoop(ctx context.Context, node *p2p.Node, blobs *store.KeyedStore) {
	const interval = 12 * time.Hour // comfortably under the ~48h record validity
	reprovide := func() {
		ids, err := blobs.List()
		if err != nil {
			return
		}
		for _, id := range ids {
			_ = node.Provide(ctx, id)
		}
	}
	reprovide()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reprovide()
		}
	}
}

// republishHeadsLoop periodically re-puts every head record this node holds on
// owners' behalf, so an owner's backups stay discoverable after the owner goes
// offline (DHT head records otherwise expire once nobody refreshes them).
func republishHeadsLoop(ctx context.Context, node *p2p.Node, heads *store.KeyedStore) {
	const interval = 12 * time.Hour // under the DHT record validity
	republish := func() {
		node.WaitForPeers(ctx, 1, time.Minute) // can't put into an empty routing table
		ownerHexes, err := heads.List()
		if err != nil {
			return
		}
		for _, ownerHex := range ownerHexes {
			val, err := heads.Get(ownerHex)
			if err != nil {
				continue
			}
			_ = node.RepublishHead(ctx, ownerHex, val)
		}
	}
	republish()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			republish()
		}
	}
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
