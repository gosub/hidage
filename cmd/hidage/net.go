package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"hidage/internal/account"
	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/friends"
	"hidage/internal/hosting"
	"hidage/internal/keyring"
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

// nodeStateLoop keeps node.json in step with the node's addresses, which change
// as relay reservations, port mappings and observed addresses come and go.
func nodeStateLoop(ctx context.Context, node *p2p.Node, locked bool) {
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
				_ = writeNodeState(addrs, locked)
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

// restoreFromFriends recovers the latest published version of your backups
// from your friends (plus the --from peers) into outDir: the head names the
// account manifest, whose roster may add friends this machine doesn't know;
// each release is rebuilt from K-of-M shards and, unless filesOnly, the restored
// folder becomes its repository again.
func restoreFromFriends(kr *keyring.Keyring, outDir string, from []string, repoSel string, all, filesOnly bool) error {
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
	manifest, err := resolveManifest(ctx, node, sources)
	if err != nil {
		return err
	}
	// The manifest's roster names friends this machine may not know yet
	// (it may be brand new): add them and ask them too, in case one holds
	// a newer head or shards the others lack.
	if added := importRoster(manifest, kr.MasterKey()); added > 0 {
		fmt.Printf("Restored %d friend(s) from your backup's friend list.\n", added)
		book, err := loadFriends()
		if err != nil {
			return err
		}
		more := peerIDs(reachFriends(ctx, node, book, false))
		sources = mergePeers(sources, more)
		if m, err := resolveManifest(ctx, node, sources); err == nil {
			manifest = m
		}
	}

	cache, err := store.NewKeyedStore(filepath.Join(config.DataStoreDir(), "cache"))
	if err != nil {
		return err
	}
	ns := netstore.New(node, cache, sources)
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
		folder, err := restoreRelease(ns, cache, kr.MasterKey(), e, outDir, !filesOnly)
		if err != nil {
			return fmt.Errorf("restore %q: %w", e.Name, err)
		}
		fmt.Printf("Recovered %q into %s\n", e.Name, folder)
	}
	if !filesOnly {
		fmt.Println("The restored folders are hidage repositories again: run `hidage backup <folder>`")
		fmt.Println("in each to resume (it only renews what your friends already hold).")
	}
	return nil
}

// listFromFriends prints the backups your account manifest indexes.
func listFromFriends(kr *keyring.Keyring, from []string) error {
	node, err := p2p.New(kr.Identity(), nil, nil)
	if err != nil {
		return err
	}
	defer node.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sources, err := recoverySources(ctx, node, from)
	if err != nil {
		return err
	}
	manifest, err := resolveManifest(ctx, node, sources)
	if err != nil {
		return err
	}
	if len(manifest.Entries) == 0 {
		fmt.Println("(no backups published to your friends)")
		return nil
	}
	fmt.Printf("%-24s  %-12s  %s\n", "NAME", "REPO-ID", "PUBLISHED")
	for _, e := range manifest.Entries {
		fmt.Printf("%-24s  %-12s  %s\n", e.Name, e.RepoID[:12], e.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	}
	return nil
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
// and restores the snapshot into outDir (under its source name), returning the
// restored folder. With adopt, the folder becomes the original repository again
// (same repo id, snapshot and objects), unless it already is one.
func restoreRelease(ns *netstore.Store, cache *store.KeyedStore, masterKey []byte, e account.Entry, outDir string, adopt bool) (string, error) {
	relBytes, err := ns.Get(e.ReleaseID)
	if err != nil {
		return "", fmt.Errorf("fetch release: %w", err)
	}
	if store.ID(relBytes) != e.ReleaseID {
		return "", fmt.Errorf("release failed integrity check")
	}
	var rel pack.Release
	if err := json.Unmarshal(relBytes, &rel); err != nil {
		return "", err
	}
	objects, err := pack.Reassemble(&rel, ns.Get)
	if err != nil {
		return "", fmt.Errorf("reassemble: %w", err)
	}
	for id, data := range objects {
		if err := cache.Put(id, data); err != nil {
			return "", err
		}
	}
	snap, err := repo.ParseSnapshot(objects[rel.SnapshotID])
	if err != nil {
		return "", err
	}
	l := dag.NewLoader(cache, cache, masterKey)
	if err := repo.RestoreWith(l, snap, outDir); err != nil {
		return "", err
	}
	name, err := repo.RootName(l, snap)
	if err != nil {
		return outDir, nil
	}
	folder := filepath.Join(outDir, name)
	if adopt && !repo.Exists(folder) {
		if _, err := repo.Adopt(folder, masterKey, config.DataStoreDir(), e.RepoID, objects[rel.SnapshotID], cache.Get); err != nil {
			return "", err
		}
	}
	return folder, nil
}

// resolveManifest fetches and verifies this node's newest account manifest from
// sources.
func resolveManifest(ctx context.Context, node *p2p.Node, sources []peer.ID) (*account.Manifest, error) {
	mid := node.ResolveHead(ctx, sources)
	if mid == "" {
		return nil, fmt.Errorf("none of the %d friend(s) reached holds a backup of yours", len(sources))
	}
	data, err := node.GetObject(ctx, mid, sources)
	if err != nil {
		return nil, fmt.Errorf("fetch account manifest: %w", err)
	}
	if store.ID(data) != mid {
		return nil, fmt.Errorf("account manifest failed integrity check")
	}
	return account.Parse(data)
}

// rosterOf lists the friends for the manifest's sealed roster.
func rosterOf(book *friends.Book) []account.RosterEntry {
	var out []account.RosterEntry
	for _, f := range book.List() {
		out = append(out, account.RosterEntry{PubKey: f.PubKey, Nickname: f.Nickname, Addrs: f.Addrs, Give: f.Give})
	}
	return out
}

// importRoster adds the manifest's roster friends that this machine doesn't
// know yet, returning how many it added.
func importRoster(m *account.Manifest, masterKey []byte) int {
	roster, err := m.OpenRoster(masterKey)
	if err != nil || len(roster) == 0 {
		return 0
	}
	book, err := loadFriends()
	if err != nil {
		return 0
	}
	added := 0
	for _, e := range roster {
		if book.IsFriend(e.PubKey) {
			continue
		}
		card := &friends.Card{PubKey: mustPub(e.PubKey), Nickname: e.Nickname}
		for _, a := range e.Addrs {
			if ma, err := multiaddr.NewMultiaddr(a); err == nil {
				card.Addrs = append(card.Addrs, ma)
			}
		}
		if _, isNew, err := book.Add(card, e.Nickname, e.Give); err == nil && isNew {
			added++
		}
	}
	return added
}

// mergePeers appends the peers in more that are not already in base.
func mergePeers(base, more []peer.ID) []peer.ID {
	seen := map[peer.ID]bool{}
	for _, p := range base {
		seen[p] = true
	}
	for _, p := range more {
		if !seen[p] {
			base, seen[p] = append(base, p), true
		}
	}
	return base
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
