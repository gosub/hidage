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
	quiet      bool // don't list unreachable friends (scheduled runs)
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
	manifest.Upsert(account.Entry{
		RepoID:    r.RepoID(),
		Name:      snap.Source,
		ReleaseID: relID,
		UpdatedAt: time.Now().UTC(),
	})
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
	placement := placeShards(rel, keys)
	assignments := make([]map[string][]byte, len(accepted))
	for i := range accepted {
		// Every peer also gets the Release and manifest, so either can always be fetched.
		assignments[i] = map[string][]byte{relID: relBytes, manifestID: manifestBytes}
		for _, id := range placement[i] {
			assignments[i][id] = shardBlobs[id]
		}
	}
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
