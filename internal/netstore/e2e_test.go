package netstore_test

import (
	"bytes"
	"context"
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
	"hidage/internal/repo"
	"hidage/internal/store"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestEndToEndCrossNodeRecovery is the Phase 3 payoff: an owner backs up, pushes
// the snapshot's objects to a storage peer and publishes a signed head pointer,
// then "loses the machine". A fresh node holding only the recovery code (hence
// the same identity + master key) rebuilds everything from the network alone.
func TestEndToEndCrossNodeRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Owner's keys come from one root (what the recovery code reproduces).
	kr, err := keyring.Init(filepath.Join(t.TempDir(), "keyring.json"), "pass")
	if err != nil {
		t.Fatal(err)
	}
	mk := kr.MasterKey()
	idA := kr.Identity()

	// Owner builds a repo + snapshot locally.
	folder := filepath.Join(t.TempDir(), "project")
	writeFile(t, filepath.Join(folder, "notes.txt"), []byte("remember the milk"))
	big := make([]byte, 1_500_000)
	rand.New(rand.NewSource(99)).Read(big)
	writeFile(t, filepath.Join(folder, "big.bin"), big)

	rA, err := repo.Init(folder, mk, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := rA.Snapshot(idA.Private, false)
	if err != nil {
		t.Fatal(err)
	}
	pubset, err := rA.PublishSet(snap)
	if err != nil {
		t.Fatal(err)
	}

	// Storage peer S: accepts pushes and serves them.
	sink, _ := store.NewKeyedStore(t.TempDir())
	idS, _ := identity.Generate()
	sNode, err := p2p.New(idS, sink, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer sNode.Close()
	sNode.EnableDHT(ctx)
	sNode.EnablePush(sink, nil)

	// Owner node A: push the object closure to S, publish the head, then leave.
	aNode, err := p2p.New(idA, nil, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	aNode.EnableDHT(ctx)
	if err := aNode.Bootstrap(ctx, sNode.Addrs()); err != nil {
		t.Fatal(err)
	}
	sNode.Bootstrap(ctx, nil)

	stored, err := aNode.PushObjects(ctx, sNode.ID(), pubset)
	if err != nil {
		t.Fatal(err)
	}
	if stored != len(pubset) {
		t.Fatalf("pushed %d objects, S stored %d", len(pubset), stored)
	}
	if err := aNode.PutHead(ctx, idA, snap.ID); err != nil {
		t.Fatal(err)
	}
	aNode.Close() // the owner's machine is gone

	// Recovered node B: same identity (from the recovery code), only the network.
	bNode, err := p2p.New(idA, nil, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer bNode.Close()
	bNode.EnableDHT(ctx)
	if err := bNode.Bootstrap(ctx, sNode.Addrs()); err != nil {
		t.Fatal(err)
	}

	// Resolve the head pointer by the owner's public key.
	var snapID string
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if snapID, _ = bNode.GetHead(ctx, idA.Public); snapID == snap.ID {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if snapID != snap.ID {
		t.Fatalf("recovered head %q, want %q", snapID, snap.ID)
	}

	// Pull the snapshot + walk the DAG, fetching every object from the network.
	cache, _ := store.NewKeyedStore(t.TempDir())
	ns := netstore.New(bNode, cache)
	snapBytes, err := ns.Get(snapID)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	recSnap, err := repo.ParseSnapshot(snapBytes)
	if err != nil {
		t.Fatal(err)
	}
	loader := dag.NewLoader(ns, ns, mk)
	out := filepath.Join(t.TempDir(), "restored")
	if err := repo.RestoreWith(loader, recSnap, out); err != nil {
		t.Fatalf("network restore: %v", err)
	}

	// Verify the recovered files.
	gotNotes, err := os.ReadFile(filepath.Join(out, "project", "notes.txt"))
	if err != nil || string(gotNotes) != "remember the milk" {
		t.Fatalf("notes.txt = %q, %v", gotNotes, err)
	}
	gotBig, err := os.ReadFile(filepath.Join(out, "project", "big.bin"))
	if err != nil || !bytes.Equal(gotBig, big) {
		t.Fatalf("big.bin mismatch (len %d), err %v", len(gotBig), err)
	}
}
