package p2p

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"hidage/internal/identity"
	"hidage/internal/store"
)

func newNode(t *testing.T, blobs Blobs) *Node {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(id, blobs, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func TestPeerIDFromIdentity(t *testing.T) {
	id, _ := identity.Generate()
	n, err := New(id, nil, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	// The peer ID must be derived from the Ed25519 public key, so two nodes from
	// the same identity get the same peer ID.
	n2, err := New(id, nil, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n.ID() != n2.ID() {
		t.Fatal("peer ID is not deterministic from the identity")
	}
}

func TestFetchObjectBetweenNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Node A holds an object; node B does not.
	storeA, _ := store.NewFSStore(t.TempDir())
	data := []byte("encrypted object bytes")
	id, _ := storeA.Put(data)

	a := newNode(t, storeA)
	storeB, _ := store.NewFSStore(t.TempDir())
	b := newNode(t, storeB)

	addrs := a.Addrs()
	if len(addrs) == 0 {
		t.Fatal("node A has no addresses")
	}
	pid, err := b.Connect(ctx, addrs[0])
	if err != nil {
		t.Fatal(err)
	}
	if pid != a.ID() {
		t.Fatalf("connected peer %s != A %s", pid, a.ID())
	}

	got, err := b.FetchObject(ctx, pid, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fetched object does not match")
	}

	// Missing object is reported cleanly.
	if _, err := b.FetchObject(ctx, pid, store.ID([]byte("absent"))); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("want ErrObjectNotFound, got %v", err)
	}
}
