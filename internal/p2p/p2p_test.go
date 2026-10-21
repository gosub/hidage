package p2p

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/hosting"
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

// newHostNode returns a node hosting friends' data with the given quota for all.
func newHostNode(t *testing.T, quota int64) (*Node, *hosting.Host) {
	t.Helper()
	h, err := hosting.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := newNodeID(t, nil)
	n.EnableHosting(h, func(string) int64 { return quota }, nil)
	return n, h
}

const testSet = "0123456789abcdef0123456789abcdef"

func TestPushSetAndFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, _ := newHostNode(t, 1<<20)
	a, _ := newNodeID(t, nil)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
	objs := map[string][]byte{}
	for _, d := range [][]byte{[]byte("obj-one"), []byte("obj-two")} {
		objs[store.ID(d)] = d
	}
	res, err := a.PushSet(ctx, s.ID(), testSet, objs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Objects != 2 || res.Sent != 2 || res.Held != 14 || res.Give != 1<<20 {
		t.Fatalf("first push = %+v", res)
	}
	// Renewing an unchanged set transfers nothing.
	if res, err := a.PushSet(ctx, s.ID(), testSet, objs); err != nil || res.Sent != 0 || res.Held != 14 {
		t.Fatalf("renewal = %+v, %v", res, err)
	}

	// S serves them back to A; GetObject tries the given peers in turn.
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
	// Another friend of S cannot read A's data.
	other.Connect(ctx, s.Addrs()[0])
	for id := range objs {
		if _, err := other.FetchObject(ctx, s.ID(), id); err == nil {
			t.Fatal("a different peer fetched A's hosted object")
		}
	}
}

func TestPushRefusedForNonFriend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, _ := newHostNode(t, 0) // quota 0: not a friend
	a, _ := newNodeID(t, nil)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PushSet(ctx, s.ID(), testSet, map[string][]byte{store.ID([]byte("x")): []byte("x")}); !errors.Is(err, ErrNotFriend) {
		t.Fatalf("err = %v, want ErrNotFriend", err)
	}
}

func TestPushQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, h := newHostNode(t, 20)
	a, _ := newNodeID(t, nil)
	if _, err := a.Connect(ctx, s.Addrs()[0]); err != nil {
		t.Fatal(err)
	}
	set := func(words ...string) map[string][]byte {
		m := map[string][]byte{}
		for _, w := range words {
			m[store.ID([]byte(w))] = []byte(w)
		}
		return m
	}
	if _, err := a.PushSet(ctx, s.ID(), testSet, set("aaaaaaaa", "bbbbbbbb")); err != nil {
		t.Fatal(err)
	}
	// Adding a third 8-byte object would hold 24 > 20 bytes.
	var qe *QuotaError
	if _, err := a.PushSet(ctx, s.ID(), testSet, set("aaaaaaaa", "bbbbbbbb", "cccccccc")); !errors.As(err, &qe) || qe.Need != 24 || qe.Give != 20 {
		t.Fatalf("err = %v, want QuotaError{24, 20}", err)
	}
	// Replacing an object frees its space: renewal never double-counts.
	for i := 0; i < 3; i++ {
		if _, err := a.PushSet(ctx, s.ID(), testSet, set("aaaaaaaa", "cccccccc")); err != nil {
			t.Fatalf("replacement %d refused: %v", i, err)
		}
	}
	aHex, _ := PeerPubKeyHex(a.ID())
	if live, trash, _ := h.Usage(aHex); live != 16 || trash != 8 {
		t.Fatalf("usage = %d live, %d trash", live, trash)
	}
}

func TestOversizedObjectRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, _ := newHostNode(t, 1<<40)
	a, _ := newNodeID(t, nil)
	a.Connect(ctx, s.Addrs()[0])
	big := make([]byte, MaxObjectSize+1)
	if _, err := a.PushSet(ctx, s.ID(), testSet, map[string][]byte{store.ID(big): big}); err == nil {
		t.Fatal("expected an object larger than MaxObjectSize to be rejected")
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

func TestPairAndFriendsOnlyHandlers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	token := bytes.Repeat([]byte{7}, 16)
	friends := map[peer.ID]bool{}
	id, _ := identity.Generate()
	inviter, err := New(id, nil, []string{"/ip4/127.0.0.1/tcp/0"}, WithFriends(func(p peer.ID) bool { return friends[p] }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inviter.Close() })
	stored := map[string][]byte{}
	inviter.EnableHeadStore(func(o string, v []byte) error { stored[o] = v; return nil }, nil)
	inviter.EnablePairing(func(from peer.ID, tok []byte, card string) (string, bool) {
		if !bytes.Equal(tok, token) || card != "invitee-card" {
			return "", false
		}
		friends[from] = true
		return "inviter-card", true
	})

	invitee, idB := newNodeID(t, nil)
	invitee.Connect(ctx, inviter.Addrs()[0])

	// Not a friend yet: head records are not accepted.
	head, _ := SignHead(idB, "m")
	if err := invitee.PushHead(ctx, inviter.ID(), head); err == nil || len(stored) != 0 {
		t.Fatal("non-friend's head record was accepted")
	}
	if _, err := invitee.Pair(ctx, inviter.ID(), bytes.Repeat([]byte{1}, 16), "invitee-card"); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("wrong token: err = %v", err)
	}
	reply, err := invitee.Pair(ctx, inviter.ID(), token, "invitee-card")
	if err != nil || reply != "inviter-card" {
		t.Fatalf("pair = %q, %v", reply, err)
	}
	if err := invitee.PushHead(ctx, inviter.ID(), head); err != nil || len(stored) != 1 {
		t.Fatalf("friend's head record refused after pairing: %v", err)
	}
}

func TestPushEntriesFetchesOnlyMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, _ := newHostNode(t, 1<<20)
	a, _ := newNodeID(t, nil)
	a.Connect(ctx, s.Addrs()[0])
	objs := map[string][]byte{}
	var entries []hosting.Entry
	for _, w := range []string{"one", "two", "three"} {
		objs[store.ID([]byte(w))] = []byte(w)
		entries = append(entries, hosting.Entry{ID: store.ID([]byte(w)), Size: int64(len(w))})
	}
	if _, err := a.PushSet(ctx, s.ID(), testSet, map[string][]byte{entries[0].ID: objs[entries[0].ID]}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	res, err := a.PushEntries(ctx, s.ID(), testSet, entries, func(id string) ([]byte, error) {
		asked = append(asked, id)
		return objs[id], nil
	})
	if err != nil || res.Sent != 2 || len(asked) != 2 {
		t.Fatalf("push = %+v, %v; asked for %d objects, want 2", res, err, len(asked))
	}
	for _, id := range asked {
		if id == entries[0].ID {
			t.Fatal("asked for an object the host already had")
		}
	}
}
