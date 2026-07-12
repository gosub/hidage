package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
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

// TestObjectRequestToNilBlobsNode ensures a node built without a blob source (as
// publish/audit/net-restore are) answers an object request cleanly instead of
// crashing on a nil dereference — any peer could otherwise take it down.
func TestObjectRequestToNilBlobsNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newNode(t, nil) // serves nothing, like a publish client
	b := newNode(t, nil)

	pid, err := b.Connect(ctx, a.Addrs()[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.FetchObject(ctx, pid, "deadbeef"); !errors.Is(err, ErrObjectNotFound) {
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
	if _, err := a.PutHead(ctx, idA, "snapshot-xyz"); err != nil {
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

func TestHeadPushAndRepublish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// Storage node S persists pushed head records; owner A hands one over.
	sink, _ := store.NewKeyedStore(t.TempDir())
	s, _ := newNodeID(t, sink)
	s.EnableDHT(ctx)
	stored := map[string][]byte{}
	s.EnableHeadStore(func(ownerHex string, val []byte) error {
		stored[ownerHex] = val
		return nil
	})

	emptyA, _ := store.NewFSStore(t.TempDir())
	a, idA := newNodeID(t, emptyA)
	a.EnableDHT(ctx)
	if err := a.Bootstrap(ctx, s.Addrs()); err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap(ctx, a.Addrs()); err != nil {
		t.Fatal(err)
	}
	s.WaitForPeers(ctx, 1, 10*time.Second) // S must have a peer to store records at

	val, err := a.PutHead(ctx, idA, "snap-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PushHead(ctx, s.ID(), HeadKey(idA.Public), val); err != nil {
		t.Fatal(err)
	}
	ownerHex := hex.EncodeToString(idA.Public)
	if _, ok := stored[ownerHex]; !ok {
		t.Fatal("storage node did not persist the pushed head record")
	}

	// A forged record (bad signature) must be rejected (the receiver drops it, so
	// the client may see no ack — what matters is it is not stored).
	_ = a.PushHead(ctx, s.ID(), HeadKey(idA.Public), []byte(`{"snapshotID":"evil","createdAt":1,"sig":"AA=="}`))
	if string(stored[ownerHex]) != string(val) {
		t.Fatal("forged head record was accepted")
	}

	// S can re-publish the owner-signed record to the DHT on A's behalf.
	if err := s.RepublishHead(ctx, ownerHex, stored[ownerHex]); err != nil {
		t.Fatalf("republish: %v", err)
	}
	var head string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if head, _ = a.GetHead(ctx, idA.Public); head == "snap-1" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if head != "snap-1" {
		t.Fatalf("GetHead after republish = %q, want snap-1", head)
	}
}

func TestPushRefusedWhenBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sink, _ := store.NewKeyedStore(t.TempDir())
	s, _ := newNodeID(t, sink)
	s.EnablePush(sink, func(pub []byte, cumulative int64) bool { return false }, nil) // refuse everyone

	emptyA, _ := store.NewFSStore(t.TempDir())
	a, _ := newNodeID(t, emptyA)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PushObjects(ctx, s.ID(), map[string][]byte{store.ID([]byte("x")): []byte("x")}); err == nil {
		t.Fatal("expected push to be refused")
	}
}

func TestPushBudgetEnforcedWithinStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Accept up to 10 cumulative bytes in the stream, then stop.
	sink, _ := store.NewKeyedStore(t.TempDir())
	s, _ := newNodeID(t, sink)
	s.EnablePush(sink, func(pub []byte, cumulative int64) bool { return cumulative <= 10 }, nil)

	emptyA, _ := store.NewFSStore(t.TempDir())
	a, _ := newNodeID(t, emptyA)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}

	// Three 8-byte objects: the first fits (8 ≤ 10), the second would reach 16
	// and must be rejected, ending the stream — so exactly one is stored.
	objs := map[string][]byte{}
	for _, w := range []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"} {
		objs[store.ID([]byte(w))] = []byte(w)
	}
	stored, err := a.PushObjects(ctx, s.ID(), objs)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("stored %d objects, want 1 (budget should have stopped the stream)", stored)
	}
}

func TestOversizedObjectRejected(t *testing.T) {
	if _, _, err := readObject(oversizedObjectFrame()); err == nil {
		t.Fatal("expected readObject to reject an object larger than MaxObjectSize")
	}
}

// oversizedObjectFrame builds a push frame declaring a data length above the cap.
func oversizedObjectFrame() *bytes.Reader {
	var buf bytes.Buffer
	id := "deadbeef"
	binary.Write(&buf, binary.BigEndian, uint32(len(id)))
	buf.WriteString(id)
	binary.Write(&buf, binary.BigEndian, uint64(MaxObjectSize+1))
	return bytes.NewReader(buf.Bytes())
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

func TestReputationQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const target = "1111111111111111111111111111111111111111111111111111111111111111"
	b, _ := newNodeID(t, nil)
	b.EnableReputationService(func(h string) (float64, float64) {
		if h == target {
			return 0.9, 5
		}
		return 0.5, 0
	})
	a, _ := newNodeID(t, nil)
	pid, err := a.Connect(ctx, b.Addrs()[0])
	if err != nil {
		t.Fatal(err)
	}

	sc, sm, err := a.AskReputation(ctx, pid, target)
	if err != nil || sc != 0.9 || sm != 5 {
		t.Fatalf("AskReputation(target) = %v,%v,%v", sc, sm, err)
	}
	// Unknown target: peer reports no evidence.
	_, sm2, err := a.AskReputation(ctx, pid, "2222222222222222222222222222222222222222222222222222222222222222")
	if err != nil || sm2 != 0 {
		t.Fatalf("AskReputation(unknown) samples = %v (%v)", sm2, err)
	}
}
