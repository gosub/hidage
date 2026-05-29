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

func TestDHTProvideAndFind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	storeA, _ := store.NewFSStore(t.TempDir())
	data := []byte("discoverable object")
	id, _ := storeA.Put(data)

	a := newNode(t, storeA)
	storeB, _ := store.NewFSStore(t.TempDir())
	b := newNode(t, storeB)

	if err := a.EnableDHT(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.EnableDHT(ctx); err != nil {
		t.Fatal(err)
	}
	// B bootstraps off A; both seed routing tables.
	if err := b.Bootstrap(ctx, a.Addrs()); err != nil {
		t.Fatal(err)
	}
	if err := a.Bootstrap(ctx, nil); err != nil {
		t.Fatal(err)
	}

	// Provide from A and discover from B, polling until the record propagates.
	var found bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !found {
		_ = a.Provide(ctx, id)
		provs, _ := b.FindProviders(ctx, id)
		for _, p := range provs {
			if p.ID == a.ID() {
				found = true
			}
		}
		if !found {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("B did not discover A as a provider via the DHT")
	}

	got, err := b.FetchObject(ctx, a.ID(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fetched object via DHT-discovered provider does not match")
	}
}

func newNodeID(t *testing.T, blobs Blobs) (*Node, identity.Identity) {
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
	return n, id
}

func TestPushTrustAndHead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// Storage node S: serves and accepts pushes into a sink.
	sink, _ := store.NewKeyedStore(t.TempDir())
	s, _ := newNodeID(t, sink)
	s.EnableDHT(ctx)
	s.EnablePush(sink, nil, nil) // accept from anyone

	// Owner node A.
	emptyA, _ := store.NewFSStore(t.TempDir())
	a, idA := newNodeID(t, emptyA)
	a.EnableDHT(ctx)
	if err := a.Bootstrap(ctx, s.Addrs()); err != nil {
		t.Fatal(err)
	}
	s.Bootstrap(ctx, nil)

	// A pushes objects to S.
	objs := map[string][]byte{}
	for _, d := range [][]byte{[]byte("obj-one"), []byte("obj-two")} {
		objs[store.ID(d)] = d
	}
	stored, err := a.PushObjects(ctx, s.ID(), objs)
	if err != nil {
		t.Fatal(err)
	}
	if stored != len(objs) {
		t.Fatalf("stored %d, want %d", stored, len(objs))
	}
	// S now serves them back to A.
	for id, want := range objs {
		got, err := a.FetchObject(ctx, s.ID(), id)
		if err != nil || string(got) != string(want) {
			t.Fatalf("fetch %s: %v", id, err)
		}
	}

	// Head pointer: A publishes, S resolves it.
	if err := a.PutHead(ctx, idA, "snapshot-xyz"); err != nil {
		t.Fatal(err)
	}
	var head string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if head, _ = s.GetHead(ctx, idA.Public); head == "snapshot-xyz" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if head != "snapshot-xyz" {
		t.Fatalf("GetHead = %q, want snapshot-xyz", head)
	}
}

func TestPushRefusedWhenBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sink, _ := store.NewKeyedStore(t.TempDir())
	s, _ := newNodeID(t, sink)
	s.EnablePush(sink, func(pub []byte) bool { return false }, nil) // refuse everyone

	emptyA, _ := store.NewFSStore(t.TempDir())
	a, _ := newNodeID(t, emptyA)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PushObjects(ctx, s.ID(), map[string][]byte{store.ID([]byte("x")): []byte("x")}); err == nil {
		t.Fatal("expected push to be refused")
	}
}

func TestAuditProofOfStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	holderStore, _ := store.NewFSStore(t.TempDir())
	data := []byte("the object the holder claims to keep")
	id, _ := holderStore.Put(data)

	holder := newNode(t, holderStore)
	challengerStore, _ := store.NewFSStore(t.TempDir())
	challenger := newNode(t, challengerStore)
	pid, err := challenger.Connect(ctx, holder.Addrs()[0])
	if err != nil {
		t.Fatal(err)
	}

	// Holder has the object and the bytes match: audit passes.
	rec, err := challenger.Audit(ctx, pid, id, data)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.OK {
		t.Fatal("audit should pass for a held, matching object")
	}

	// Object the holder does not have: audit fails (not OK).
	rec, err = challenger.Audit(ctx, pid, store.ID([]byte("absent")), []byte("absent"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.OK {
		t.Fatal("audit should fail for a missing object")
	}

	// Holder has the object but the challenger expects different bytes
	// (i.e. the holder is serving the wrong/corrupted data): audit fails.
	rec, err = challenger.Audit(ctx, pid, id, []byte("different expected bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.OK {
		t.Fatal("audit should fail when the proof does not match expected bytes")
	}
}
