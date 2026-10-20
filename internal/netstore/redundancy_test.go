package netstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hidage/internal/dag"
	"hidage/internal/identity"
	"hidage/internal/keyring"
	"hidage/internal/netstore"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/store"
)

// TestRedundantRecoverySurvivesPeerLoss is the Phase 4d payoff: a snapshot is
// erasure-coded into shards spread across three storage peers; one peer is then
// lost entirely, yet a recovering node still rebuilds every file from the K-of-M
// shards remaining on the others.
func TestRedundantRecoverySurvivesPeerLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	kr, _ := keyring.Init(filepath.Join(t.TempDir(), "keyring.json"), "pass")
	mk := kr.MasterKey()
	idA := kr.Identity()

	// Owner builds a repo + snapshot.
	folder := filepath.Join(t.TempDir(), "project")
	writeFile(t, filepath.Join(folder, "notes.txt"), []byte("survive a peer loss"))
	big := make([]byte, 1_000_000)
	rand.New(rand.NewSource(5)).Read(big)
	writeFile(t, filepath.Join(folder, "big.bin"), big)
	rA, _ := repo.Init(folder, mk, t.TempDir())
	snap, _, err := rA.Snapshot(idA.Private, false)
	if err != nil {
		t.Fatal(err)
	}
	objs, _ := rA.PublishSet(snap)

	// Erasure-code at 3x and prepare the Release.
	rel, shardBlobs, err := pack.Build(objs, 3.0, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	relBytes, _ := json.Marshal(rel)
	relID := store.ID(relBytes)

	// Three storage peers in one DHT.
	storNodes := make([]*p2p.Node, 3)
	for i := range storNodes {
		sink, _ := store.NewKeyedStore(t.TempDir())
		id, _ := identity.Generate()
		n, err := p2p.New(id, sink, []string{"/ip4/127.0.0.1/tcp/0"})
		if err != nil {
			t.Fatal(err)
		}
		n.EnableDHT(ctx)
		n.EnablePush(sink, nil, nil)
		storNodes[i] = n
	}
	defer func() {
		for _, n := range storNodes {
			n.Close()
		}
	}()
	for i := 1; i < 3; i++ {
		if err := storNodes[i].Bootstrap(ctx, storNodes[0].Addrs()); err != nil {
			t.Fatal(err)
		}
	}
	storNodes[0].Bootstrap(ctx, nil)

	// Owner spreads shards round-robin across the three peers, plus the Release.
	owner, _ := p2p.New(idA, nil, []string{"/ip4/127.0.0.1/tcp/0"})
	owner.EnableDHT(ctx)
	owner.Bootstrap(ctx, storNodes[0].Addrs())
	assign := make([]map[string][]byte, 3)
	for i := range assign {
		assign[i] = map[string][]byte{relID: relBytes}
	}
	j := 0
	for id, data := range shardBlobs {
		assign[j%3][id] = data
		j++
	}
	for i, n := range storNodes {
		if _, err := owner.PushObjects(ctx, n.ID(), assign[i]); err != nil {
			t.Fatalf("push to peer %d: %v", i, err)
		}
	}
	if _, err := owner.PutHead(ctx, idA, relID); err != nil {
		t.Fatal(err)
	}
	owner.Close()

	// Lose a peer entirely.
	storNodes[1].Close()

	// Recover from the survivors (bootstrap off both remaining peers).
	rec, _ := p2p.New(idA, nil, []string{"/ip4/127.0.0.1/tcp/0"})
	defer rec.Close()
	rec.EnableDHT(ctx)
	rec.Bootstrap(ctx, append(storNodes[0].Addrs(), storNodes[2].Addrs()...))

	var got string
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ = rec.GetHead(ctx, idA.Public); got == relID {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if got != relID {
		t.Fatalf("recovered head %q, want %q", got, relID)
	}

	cache, _ := store.NewKeyedStore(t.TempDir())
	ns := netstore.New(rec, cache)
	fetchedRel, err := ns.Get(relID)
	if err != nil {
		t.Fatalf("fetch release: %v", err)
	}
	var rel2 pack.Release
	json.Unmarshal(fetchedRel, &rel2)
	objects, err := pack.Reassemble(&rel2, ns.Get)
	if err != nil {
		t.Fatalf("reassemble after peer loss: %v", err)
	}
	for id, data := range objects {
		cache.Put(id, data)
	}
	recSnap, err := repo.ParseSnapshot(objects[rel2.SnapshotID])
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "restored")
	if err := repo.RestoreWith(dag.NewLoader(cache, cache, mk), recSnap, out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gotBig, err := os.ReadFile(filepath.Join(out, "project", "big.bin"))
	if err != nil || !bytes.Equal(gotBig, big) {
		t.Fatalf("big.bin mismatch after peer loss (err %v)", err)
	}
}
