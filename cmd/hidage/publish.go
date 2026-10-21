package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/account"
	"hidage/internal/config"
	"hidage/internal/friends"
	"hidage/internal/hosting"
	"hidage/internal/keyring"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/store"
)

// publishOpts are the knobs of one publish.
type publishOpts struct {
	peers      []string // explicit peer multiaddrs instead of friends
	redundancy float64
	minPeers   int
	quiet      bool           // don't list unreachable friends (scheduled runs)
	retention  repo.Retention // which older versions friends keep too
}

// retentionPolicy converts the config's retention settings.
func retentionPolicy(s config.Settings) repo.Retention {
	return repo.Retention{KeepLast: s.Retention.KeepLast, KeepDaily: s.Retention.KeepDaily, KeepWeekly: s.Retention.KeepWeekly}
}

// applyRetention forgets the snapshots the policy doesn't keep and frees their
// space, returning how many it forgot.
func applyRetention(r *repo.Repo, policy repo.Retention, dryRun bool, out io.Writer) (int, error) {
	snaps, err := r.ListSnapshots()
	if err != nil {
		return 0, err
	}
	keep := policy.Retained(snaps)
	n := 0
	for _, s := range snaps {
		if keep[s.ID] {
			continue
		}
		n++
		if dryRun {
			fmt.Fprintf(out, "would forget %s (%s)\n", s.ID[:12], s.CreatedAt.Local().Format("2006-01-02 15:04"))
			continue
		}
		if err := r.Forget(s.ID); err != nil {
			return n, err
		}
	}
	if n > 0 && !dryRun {
		stats, err := r.Prune()
		if err != nil {
			return n, err
		}
		fmt.Fprintf(out, "retention: forgot %d old snapshot(s), freed %d objects\n", n, stats.Deleted)
	}
	return n, nil
}

// version is one snapshot being published: its Release and, once built, its
// shard bytes (older versions are rebuilt only if a friend lacks a shard).
type version struct {
	snap     *repo.Snapshot
	rel      *pack.Release
	relBytes []byte
	relID    string
	shards   map[string][]byte
}

// olderVersions returns the Releases of the retained snapshots other than
// latest, newest first, from the release cache (building and caching any
// that were never published).
func olderVersions(r *repo.Repo, latest *repo.Snapshot, policy repo.Retention, redundancy float64) ([]*version, error) {
	snaps, err := r.ListSnapshots()
	if err != nil {
		return nil, err
	}
	keep := policy.Retained(snaps)
	var out []*version
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		if s.ID == latest.ID || !keep[s.ID] {
			continue
		}
		v := &version{snap: s}
		if v.relBytes, err = r.LoadRelease(s.ID); err != nil {
			objs, err := r.PublishSet(s)
			if err != nil {
				return nil, err
			}
			rel, shards, err := pack.Build(objs, redundancy, s.ID)
			if err != nil {
				return nil, err
			}
			if v.relBytes, err = json.Marshal(rel); err != nil {
				return nil, err
			}
			v.shards = shards
			r.SaveRelease(s.ID, v.relBytes)
		}
		v.rel = new(pack.Release)
		if err := json.Unmarshal(v.relBytes, v.rel); err != nil {
			return nil, fmt.Errorf("cached release of %s: %w", s.ID[:12], err)
		}
		v.relID = store.ID(v.relBytes)
		out = append(out, v)
	}
	return out, nil
}

// shardData returns a version's shard, rebuilding the version's shards (with
// the redundancy it was built with) the first time one is needed.
func (v *version) shardData(r *repo.Repo, id string) ([]byte, error) {
	if v.shards == nil {
		objs, err := r.PublishSet(v.snap)
		if err != nil {
			return nil, err
		}
		rel, shards, err := pack.Build(objs, v.rel.Redundancy, v.snap.ID)
		if err != nil {
			return nil, err
		}
		if b, _ := json.Marshal(rel); store.ID(b) != v.relID {
			return nil, fmt.Errorf("version %s rebuilds differently; forget it or publish again", v.snap.ID[:12])
		}
		v.shards = shards
	}
	data, ok := v.shards[id]
	if !ok {
		return nil, fmt.Errorf("no shard %s in version %s", id, v.snap.ID[:12])
	}
	return data, nil
}

// publishRepo erasure-codes r's latest snapshot, replaces its set on every
// reachable friend (or opts.peers), records where the shards went, updates the
// account manifest and hands friends the new signed head. Progress goes to out.
func publishRepo(ctx context.Context, node *p2p.Node, kr *keyring.Keyring, r *repo.Repo, book *friends.Book, opts publishOpts, out io.Writer) error {
	peers, redundancy, minPeers, quiet := opts.peers, opts.redundancy, opts.minPeers, opts.quiet
	snap, err := r.Latest()
	if err != nil {
		return err
	}
	if snap == nil {
		return fmt.Errorf("no snapshots to publish")
	}
	// Check where to publish before the expensive packing and networking.
	useFriends := len(peers) == 0
	if useFriends && len(book.List()) == 0 {
		return fmt.Errorf("no friends to publish to; add one with `hidage friend add <card>` (or pass --peer)")
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
	if err := r.SaveRelease(snap.ID, relBytes); err != nil {
		return err
	}
	versions := []*version{{snap: snap, rel: rel, relBytes: relBytes, relID: relID, shards: shardBlobs}}
	older, err := olderVersions(r, snap, opts.retention, redundancy)
	if err != nil {
		return err
	}
	versions = append(versions, older...)

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
			fmt.Fprintf(out, "  %s: %v\n", addr, err)
			continue
		}
		add(pid)
	}
	var reached []friends.Friend
	if useFriends {
		reached = reachFriends(ctx, node, book, quiet)
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
	entry := account.Entry{RepoID: r.RepoID(), Name: snap.Source, ReleaseID: relID, UpdatedAt: time.Now().UTC()}
	for _, v := range older {
		entry.History = append(entry.History, v.relID)
	}
	manifest.Upsert(entry)
	// Seal the friend list into the manifest, so recovering from any one
	// friend finds all of them.
	if err := manifest.SetRoster(kr.MasterKey(), rosterOf(book)); err != nil {
		return err
	}
	manifestBytes, err := manifest.Bytes()
	if err != nil {
		return err
	}
	manifestID := store.ID(manifestBytes)
	// Erasure coding only helps if shards land on enough distinct peers.
	tolerate := int(float64(len(accepted)) * (1 - 1/redundancy))
	if tolerate == 0 {
		fmt.Fprintf(out, "WARNING: %d peer(s) at %.1fx redundancy gives no fault tolerance "+
			"(losing one peer loses the backup); use more peers or higher --redundancy.\n",
			len(accepted), redundancy)
	} else {
		fmt.Fprintf(out, "Spreading across %d peers; tolerates losing up to %d.\n", len(accepted), tolerate)
	}

	keys := make([]string, len(accepted))
	for i, p := range accepted {
		keys[i] = p.pubHex
	}
	// Each friend's set: the manifest, and for every version its Release and
	// the shards placed on that friend. Shared shards appear once.
	sets := make([]map[string]int64, len(accepted))
	for i := range accepted {
		sets[i] = map[string]int64{manifestID: int64(len(manifestBytes))}
	}
	owner := map[string]*version{} // shard id → a version that has it
	var latestPlacement [][]string
	for vi, v := range versions {
		placement := placeShards(v.rel, keys)
		if vi == 0 {
			latestPlacement = placement
		}
		sizes := map[string]int64{}
		for _, desc := range v.rel.Packs {
			for _, ref := range desc.Shards {
				sizes[ref.ID] = ref.Size
				if owner[ref.ID] == nil {
					owner[ref.ID] = v
				}
			}
		}
		for i := range accepted {
			sets[i][v.relID] = int64(len(v.relBytes))
			for _, id := range placement[i] {
				sets[i][id] = sizes[id]
			}
		}
	}
	plain := map[string][]byte{manifestID: manifestBytes}
	for _, v := range versions {
		plain[v.relID] = v.relBytes
	}
	get := func(id string) ([]byte, error) {
		if d, ok := plain[id]; ok {
			return d, nil
		}
		if v := owner[id]; v != nil {
			return v.shardData(r, id)
		}
		return nil, fmt.Errorf("unknown object %s", id)
	}
	if len(versions) > 1 {
		fmt.Fprintf(out, "Publishing %d versions (the latest and %d older ones your retention keeps).\n", len(versions), len(versions)-1)
	}
	dist := distribution{ReleaseID: relID, SnapshotID: snap.ID, Redundancy: redundancy, PublishedAt: time.Now().UTC(), Peers: map[string][]string{}}
	setID := hostedSetID(kr.MasterKey(), r.RepoID())
	var holders []peerID
	for i, p := range accepted {
		shardIDs := latestPlacement[i]
		var entries []hosting.Entry
		for id, size := range sets[i] {
			entries = append(entries, hosting.Entry{ID: id, Size: size})
		}
		name := peerName(book, p)
		res, err := node.PushEntries(ctx, p.id, setID, entries, get)
		var qe *p2p.QuotaError
		switch {
		case errors.As(err, &qe):
			fmt.Fprintf(out, "  %s: not enough space: this backup needs %s there, they give you %s\n",
				name, config.FormatSize(qe.Need), config.FormatSize(qe.Give))
			continue
		case err != nil:
			fmt.Fprintf(out, "  %s: %v\n", name, err)
			continue
		}
		dist.Peers[p.pubHex] = shardIDs // remember what to audit them for
		holders = append(holders, p)
		book.Update(p.pubHex, func(f *friends.Friend) error {
			f.StoredThere, f.QuotaThere = res.Held, res.Give
			return nil
		})
		fmt.Fprintf(out, "  %s: %d shards, sent %d new (%s); you use %s of %s there\n",
			name, len(shardIDs), res.Sent, config.FormatSize(res.SentBytes),
			config.FormatSize(res.Held), config.FormatSize(res.Give))
	}
	// Shards assigned to a peer that didn't store them are missing; the
	// release is only rebuildable if no more peers failed than it tolerates.
	if failed := len(accepted) - len(holders); failed > tolerate || len(holders) == 0 {
		return fmt.Errorf("only %d of %d peers stored their shards, too few to rebuild the backup; "+
			"the previous publish (if any) is still the one on record", len(holders), len(accepted))
	} else if failed > 0 {
		fmt.Fprintf(out, "WARNING: %d peer(s) did not store their shards; the backup is rebuildable but has less fault tolerance than planned.\n", failed)
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
	fmt.Fprintf(out, "Published %q (%s) as %d shards across %d peers (%.1fx redundancy).\n",
		snap.Source, r.RepoID()[:12], len(shardBlobs), len(holders), redundancy)
	fmt.Fprintf(out, "Account now indexes %d backup(s).\n", len(manifest.Entries))
	return nil
}
