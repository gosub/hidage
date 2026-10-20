package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/identity"
	"hidage/internal/store"
)

func newNode(t *testing.T, blobs Blobs) *Node {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(id, blobs, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func TestPeerIDFromIdentity(t *testing.T) {
	id, _ := identity.Generate()
	n, err := New(id, nil, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	// The peer ID must be derived from the Ed25519 public key, so two nodes from
	// the same identity get the same peer ID.
	n2, err := New(id, nil, []string{"/ip4/127.0.0.1/tcp/0"})
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

func newNodeID(t *testing.T, blobs Blobs) (*Node, identity.Identity) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(id, blobs, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n, id
}

func TestPushAndFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Storage node S: serves and accepts pushes into a sink.
	sink, _ := store.NewKeyedStore(t.TempDir())
	s, _ := newNodeID(t, sink)
	s.EnablePush(sink, nil, nil) // accept from anyone

	a, _ := newNodeID(t, nil)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
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
	// S now serves them back; GetObject tries the given peers in turn.
	other, _ := newNodeID(t, nil)
	a.Connect(ctx, other.Addrs()[0])
	for id, want := range objs {
		got, err := a.GetObject(ctx, id, []peer.ID{other.ID(), s.ID()})
		if err != nil || string(got) != string(want) {
			t.Fatalf("get %s: %v", id, err)
		}
	}
	if _, err := a.GetObject(ctx, store.ID([]byte("nope")), []peer.ID{s.ID()}); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing object: err = %v", err)
	}
}

func TestHeadPushAndResolve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Two friends S1, S2 keep pushed head records; owner A hands records over.
	newHolder := func() (*Node, map[string][]byte) {
		n, _ := newNodeID(t, nil)
		stored := map[string][]byte{}
		n.EnableHeadStore(
			func(ownerHex string, val []byte) error { stored[ownerHex] = val; return nil },
			func(ownerHex string) ([]byte, error) { return stored[ownerHex], nil })
		return n, stored
	}
	s1, stored1 := newHolder()
	s2, _ := newHolder()

	a, idA := newNodeID(t, nil)
	for _, s := range []*Node{s1, s2} {
		if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := SignHead(idA, "manifest-1")
	if err := a.PushHead(ctx, s1.ID(), old); err != nil {
		t.Fatal(err)
	}
	newer, _ := SignHead(idA, "manifest-2")
	if err := a.PushHead(ctx, s2.ID(), newer); err != nil {
		t.Fatal(err)
	}
	ownerHex := hex.EncodeToString(idA.Public)
	if _, ok := stored1[ownerHex]; !ok {
		t.Fatal("holder did not persist the pushed head record")
	}

	// A forged record (bad signature) is not stored.
	if err := a.PushHead(ctx, s1.ID(), []byte(`{"snapshotID":"evil","createdAt":1,"sig":"AA=="}`)); err == nil {
		t.Fatal("forged head record was acknowledged")
	}
	// Another peer can't set A's head by replaying A's record under its own name.
	b, _ := newNodeID(t, nil)
	b.Connect(ctx, s1.Addrs()[0])
	if err := b.PushHead(ctx, s1.ID(), newer); err == nil {
		t.Fatal("record signed by someone else was acknowledged")
	}
	if string(stored1[ownerHex]) != string(old) {
		t.Fatal("holder's record for A changed")
	}

	// A (e.g. recovering on a new machine, same key) resolves the newest head.
	if got := a.ResolveHead(ctx, []peer.ID{s1.ID(), s2.ID()}); got != "manifest-2" {
		t.Fatalf("ResolveHead = %q, want manifest-2", got)
	}
	// Holders answer only the owner: B gets nothing for itself.
	if got := b.ResolveHead(ctx, []peer.ID{s1.ID()}); got != "" {
		t.Fatalf("B resolved %q, want nothing", got)
	}
}

func TestAddrRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// B knows C's signed record; A asks B for it.
	b, _ := newNodeID(t, nil)
	c, _ := newNodeID(t, nil)
	a, _ := newNodeID(t, nil)
	cHex, _ := PeerPubKeyHex(c.ID())
	known := map[string]*AddrRecord{}
	b.EnableAddrService(func(subject string) *AddrRecord { return known[subject] })
	c.EnableAddrService(nil)

	// B fetches C's own record directly from C and keeps it.
	b.Connect(ctx, c.Addrs()[0])
	rec, err := b.FetchAddrRecord(ctx, c.ID(), cHex)
	if err != nil || rec == nil || len(rec.Addrs) == 0 {
		t.Fatalf("self record: %+v, %v", rec, err)
	}
	known[cHex] = rec

	a.Connect(ctx, b.Addrs()[0])
	got, err := a.FetchAddrRecord(ctx, b.ID(), cHex)
	if err != nil || got == nil || got.Seq != rec.Seq {
		t.Fatalf("relayed record: %+v, %v", got, err)
	}
	if err := a.ConnectAddrs(ctx, c.ID(), got.Addrs); err != nil {
		t.Fatalf("connect with relayed addresses: %v", err)
	}

	// A tampered record is rejected.
	bad := *rec
	bad.Addrs = []string{"/ip4/203.0.113.9/tcp/1"}
	known[cHex] = &bad
	if _, err := a.FetchAddrRecord(ctx, b.ID(), cHex); err == nil {
		t.Fatal("tampered address record accepted")
	}
	// Unknown subjects yield nil.
	if r, err := a.FetchAddrRecord(ctx, b.ID(), strings.Repeat("ab", 32)); err != nil || r != nil {
		t.Fatalf("unknown subject: %+v, %v", r, err)
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

func TestInboundFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	friend, _ := newNodeID(t, nil)
	stranger, _ := newNodeID(t, nil)
	id, _ := identity.Generate()
	gated, err := New(id, nil, []string{"/ip4/127.0.0.1/tcp/0"}, WithInboundFilter(func(p peer.ID) bool {
		return p == friend.ID()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gated.Close() })

	if _, err := friend.Connect(ctx, gated.Addrs()[0]); err != nil {
		t.Fatalf("friend should be admitted: %v", err)
	}
	if _, err := stranger.Connect(ctx, gated.Addrs()[0]); err == nil {
		// The dial may complete before the gater's close propagates; a stream must fail.
		if _, err := stranger.FetchObject(ctx, gated.ID(), "00"); err == nil {
			t.Fatal("stranger should be refused")
		}
	}
	// Outbound dials from the gated node are not filtered.
	if _, err := gated.Connect(ctx, stranger.Addrs()[0]); err != nil {
		t.Fatalf("outbound dial to a non-friend should work: %v", err)
	}
}
