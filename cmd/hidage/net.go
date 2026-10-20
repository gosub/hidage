package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"hidage/internal/account"
	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/friends"
	"hidage/internal/hosting"
	"hidage/internal/netstore"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/store"
)

// hostedStore is where this node keeps its friends' data.
func hostedStore() (*hosting.Host, error) {
	return hosting.Open(filepath.Join(config.DataStoreDir(), "hosted"))
}

// headsStore holds the signed head records friends handed this node, keyed by
// owner public-key hex, so an owner can get its head back after losing its machine.
func headsStore() (*store.KeyedStore, error) {
	return store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "heads"))
}

func newServeCmd() *cobra.Command {
	var listen []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a node: host and serve friends' data, relay for them, keep in touch",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			settings, _ := config.Load()
			if !cmd.Flags().Changed("listen") {
				listen = settings.Listen
			}
			reach, err := p2p.ParseReachability(settings.Reachability)
			if err != nil {
				return err
			}
			hosted, err := hostedStore()
			if err != nil {
				return err
			}
			book, err := loadFriends()
			if err != nil {
				return err
			}
			isFriend := func(id peer.ID) bool {
				book.Refresh() // pick up `friend add/rm` made while serving
				return book.IsFriendPeer(id)
			}
			invites, err := openInvites()
			if err != nil {
				return err
			}
			// While an invite is open, strangers may connect, but only to pair:
			// every other protocol checks friendship (WithFriends, hosting quota).
			admit := func(id peer.ID) bool { return isFriend(id) || invites.Pending() }
			if reach == p2p.ReachabilityAuto && !hasPublicInterface() {
				// No public IP on any interface: behind a NAT unless a port is
				// forwarded (then set reachability = "public"). Deciding now, rather
				// than waiting for AutoNAT, which needs several peers to agree,
				// gets relay reservations in place even in a two-friend group.
				reach = p2p.ReachabilityPrivate
			}
			node, err := p2p.New(kr.Identity(), nil, listen,
				p2p.WithInboundFilter(admit), // friends only (and invitees while an invite is open)
				p2p.WithFriends(isFriend),
				p2p.WithRelayService(isFriend),  // relay for friends while reachable
				p2p.WithRelayCandidates(func() []peer.AddrInfo { return friendAddrInfos(book) }),
				p2p.WithReachability(reach))
			if err != nil {
				return err
			}
			defer node.Close()

			ctx := context.Background()
			heads, err := headsStore()
			if err != nil {
				return err
			}
			node.EnableHeadStore(heads.Put, heads.Get) // owner-signed; returned to the owner on recovery
			node.EnableAddrService(func(subject string) *p2p.AddrRecord { return storedAddrRecord(book, subject) })
			node.EnablePairing(func(from peer.ID, token []byte, cardStr string) (string, bool) {
				card, err := friends.DecodeCard(cardStr)
				if err != nil {
					return "", false
				}
				if pid, err := friends.PeerIDFromHex(hex.EncodeToString(card.PubKey)); err != nil || pid != from {
					return "", false // the card must be the requester's own
				}
				inv, ok := invites.Consume(token)
				if !ok {
					return "", false
				}
				card.Token = nil
				f, _, err := book.Add(card, inv.Nickname, inv.Give)
				if err != nil {
					return "", false
				}
				fmt.Printf("[friends] %s accepted your invite and is now a friend\n", f.Nickname)
				return cardFor(kr.Identity().Public, node.ListenAddrs()).Encode(), true
			})
			node.EnableHosting(hosted,
				func(ownerHex string) int64 {
					f, ok := book.Get(ownerHex)
					if !ok {
						return 0 // friends only
					}
					return f.Give
				},
				func(ownerHex string, held int64) {
					book.Update(ownerHex, func(f *friends.Friend) error { f.LastSeen = time.Now().UTC(); return nil })
				})
			go keepInTouchLoop(ctx, node, book)
			go reapLoop(ctx, hosted, time.Duration(settings.Hosting.LeaseDays)*24*time.Hour,
				time.Duration(settings.Hosting.TrashDays)*24*time.Hour)

			if err := writeNodeState(node.ListenAddrs()); err != nil {
				return err
			}
			defer os.Remove(nodeStatePath())
			go nodeStateLoop(ctx, node) // relay and port-mapped addresses appear later
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
	cmd.Flags().StringArrayVar(&listen, "listen", nil, "listen multiaddr(s) (default: `listen` in config.toml)")
	return cmd
}

// nodeStateLoop keeps node.json in step with the node's addresses, which change
// as relay reservations, port mappings and observed addresses come and go.
func nodeStateLoop(ctx context.Context, node *p2p.Node) {
	last := strings.Join(node.ListenAddrs(), " ")
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			addrs := node.ListenAddrs()
			if cur := strings.Join(addrs, " "); cur != last {
				last = cur
				_ = writeNodeState(addrs)
			}
		}
	}
}

// keepInTouchLoop periodically reconnects to friends (so they can relay for us
// and we for them), refreshes their signed address records, and presents any
// invite we accepted while the inviter was unreachable.
func keepInTouchLoop(ctx context.Context, node *p2p.Node, book *friends.Book) {
	for i := 0; ; i++ {
		book.Refresh()
		completePairings(ctx, node, book)
		if i%10 == 0 {
			reachFriends(ctx, node, book, true)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second): // reach friends every 5 minutes, pair sooner
		}
	}
}

// reapLoop periodically drops friends' sets not renewed within the lease and
// deletes trash older than the grace period.
func reapLoop(ctx context.Context, h *hosting.Host, lease, trashAge time.Duration) {
	for {
		if st, err := h.Reap(lease, trashAge); err == nil && st.ExpiredSets+st.DeletedTrash > 0 {
			fmt.Printf("[hosting] %d expired backup set(s); deleted %d old trashed object(s) (%s)\n",
				st.ExpiredSets, st.DeletedTrash, config.FormatSize(st.FreedBytes))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func newPublishCmd() *cobra.Command {
	var peers []string
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

			node, err := p2p.New(kr.Identity(), nil, nil)
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

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
			var reached []friends.Friend
			if useFriends {
				reached = reachFriends(ctx, node, book, false)
				for _, pid := range peerIDs(reached) {
					add(pid)
				}
			}
			if len(accepted) == 0 {
				return fmt.Errorf("no friends reachable to publish to (see `hidage friend ls`)")
			}
			if len(accepted) < minPeers {
				return fmt.Errorf("only %d acceptable peer(s) reachable, need at least --min-peers=%d", len(accepted), minPeers)
			}

			// Merge this repo's release into the owner's account manifest, so the
			// single head pointer indexes every backed-up folder, not just this one.
			manifest := fetchManifest(ctx, node, peerIDs(reached))
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
			setID := hostedSetID(kr.MasterKey(), r.RepoID())
			var holders []peerID
			for i, p := range accepted {
				push := assignments[i]
				var shardIDs []string
				for id := range push {
					if _, isShard := shardBlobs[id]; isShard {
						shardIDs = append(shardIDs, id)
					}
				}
				name := peerName(book, p)
				res, err := node.PushSet(ctx, p.id, setID, push)
				var qe *p2p.QuotaError
				switch {
				case errors.As(err, &qe):
					fmt.Printf("  %s: not enough space: this backup needs %s there, they give you %s\n",
						name, config.FormatSize(qe.Need), config.FormatSize(qe.Give))
					continue
				case err != nil:
					fmt.Printf("  %s: %v\n", name, err)
					continue
				}
				dist.Peers[p.pubHex] = shardIDs // remember what to audit them for
				holders = append(holders, p)
				book.Update(p.pubHex, func(f *friends.Friend) error {
					f.StoredThere, f.QuotaThere = res.Held, res.Give
					return nil
				})
				fmt.Printf("  %s: %d shards, sent %d new (%s); you use %s of %s there\n",
					name, len(shardIDs), res.Sent, config.FormatSize(res.SentBytes),
					config.FormatSize(res.Held), config.FormatSize(res.Give))
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
			headVal, err := p2p.SignHead(kr.Identity(), manifestID)
			if err != nil {
				return err
			}
			// Hand the signed head to every friend reached (not just this repo's
			// holders: the manifest indexes all repos), so it can be asked back
			// during recovery.
			for _, pid := range peerIDs(reached) {
				_ = node.PushHead(ctx, pid, headVal)
			}
			for _, p := range holders {
				if !seenFriend(reached, p.pubHex) {
					_ = node.PushHead(ctx, p.id, headVal)
				}
			}
			fmt.Printf("Published %q (%s) as %d shards across %d peers (%.1fx redundancy).\n",
				snap.Source, r.RepoID()[:12], len(shardBlobs), len(holders), redundancy)
			fmt.Printf("Account now indexes %d backup(s).\n", len(manifest.Entries))
			return nil
		},
	}
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

// peerName is a friend's nickname, or a short peer ID for others.
func peerName(book *friends.Book, p peerID) string {
	if f, ok := book.Get(p.pubHex); ok {
		return f.Nickname
	}
	return p.id.String()[:16]
}

// hostedSetID names a repository's set on friends' nodes. It is derived from the
// repo id under the master key, so hosts can't link it to the repo.
func hostedSetID(masterKey []byte, repoID string) string {
	m := hmac.New(sha256.New, masterKey)
	m.Write([]byte("hidage/hosted-set/v1\n" + repoID))
	return hex.EncodeToString(m.Sum(nil))[:32]
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

// seenFriend reports whether pubHex is among the friends in list.
func seenFriend(list []friends.Friend, pubHex string) bool {
	for _, f := range list {
		if f.PubKey == pubHex {
			return true
		}
	}
	return false
}

// fetchManifest resolves this node's current account manifest from its friends
// (newest signed head wins), returning an empty one if none exists or it can't
// be verified.
func fetchManifest(ctx context.Context, node *p2p.Node, peers []peer.ID) *account.Manifest {
	mid := node.ResolveHead(ctx, peers)
	if mid == "" {
		return account.New()
	}
	data, err := node.GetObject(ctx, mid, peers)
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
	var from []string
	var repoSel string
	var all bool
	cmd := &cobra.Command{
		Use:   "net-restore <out-dir>",
		Short: "Recover backups from your friends (head → account manifest → shards)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			node, err := p2p.New(kr.Identity(), nil, nil)
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			sources, err := recoverySources(ctx, node, from)
			if err != nil {
				return err
			}
			mid := node.ResolveHead(ctx, sources)
			if mid == "" {
				return fmt.Errorf("none of the %d friend(s) reached holds a backup of yours", len(sources))
			}

			cache, err := store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "cache"))
			if err != nil {
				return err
			}
			ns := netstore.New(node, cache, sources)

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
				return fmt.Errorf("you have no published backups")
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
	cmd.Flags().StringArrayVar(&from, "from", nil, "also recover from this friend (contact card or /p2p multiaddr), e.g. on a new machine")
	cmd.Flags().StringVar(&repoSel, "repo", "", "which backup to restore (name or repo-id prefix)")
	cmd.Flags().BoolVar(&all, "all", false, "restore every backup in the account")
	return cmd
}

// recoverySources reaches every friend in the friend list plus the extra peers
// given with --from, and returns them all. It fails if none can be reached.
func recoverySources(ctx context.Context, node *p2p.Node, from []string) ([]peer.ID, error) {
	extra, err := connectExtra(ctx, node, from)
	if err != nil {
		return nil, err
	}
	book, err := loadFriends()
	if err != nil {
		return nil, err
	}
	sources := append(peerIDs(reachFriends(ctx, node, book, true)), extra...)
	if len(sources) == 0 {
		if len(book.List()) == 0 {
			return nil, fmt.Errorf("no friends to recover from; on a new machine pass --from <a friend's contact card or address>")
		}
		return nil, fmt.Errorf("none of your friends is reachable right now")
	}
	return sources, nil
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

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
