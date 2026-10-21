package friends

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multiformats/go-multiaddr"
)

func newCard(t *testing.T, nick string, addrs ...string) *Card {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Card{PubKey: pub, Nickname: nick}
	for _, a := range addrs {
		c.Addrs = append(c.Addrs, multiaddr.StringCast(a))
	}
	return c
}

func TestCardRoundTrip(t *testing.T) {
	c := newCard(t, "alice", "/ip4/192.0.2.1/tcp/4001", "/ip6/2001:db8::1/udp/4001/quic-v1")
	s := c.Encode()
	if !strings.HasPrefix(s, CardPrefix) {
		t.Fatalf("card %q lacks prefix", s)
	}
	// Line wrapping and upper-casing are tolerated.
	got, err := DecodeCard(strings.ToUpper(s[:20]) + "\n  " + s[20:])
	if err != nil {
		t.Fatal(err)
	}
	if !got.PubKey.Equal(c.PubKey) || got.Nickname != "alice" || len(got.Addrs) != 2 ||
		!got.Addrs[1].Equal(c.Addrs[1]) {
		t.Fatalf("decoded %+v, want %+v", got, c)
	}
}

func TestCardDamageDetected(t *testing.T) {
	s := newCard(t, "bob", "/ip4/192.0.2.1/tcp/4001").Encode()
	mid := len(s) / 2
	flip := byte('a')
	if s[mid] == 'a' {
		flip = 'b'
	}
	damaged := s[:mid] + string(flip) + s[mid+1:]
	for _, bad := range []string{damaged, s[:30], "hello", strings.TrimPrefix(s, CardPrefix)} {
		if _, err := DecodeCard(bad); err == nil {
			t.Errorf("DecodeCard(%q) should fail", bad)
		}
	}
}

func TestBookAddResolvePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "friends.json")
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	alice := newCard(t, "alice", "/ip4/192.0.2.1/tcp/4001")
	f, isNew, err := b.Add(alice, "", 1<<30)
	if err != nil || !isNew || f.Nickname != "alice" || f.Give != 1<<30 {
		t.Fatalf("Add = %+v, %v, %v", f, isNew, err)
	}
	// A second "alice" with a different key gets a unique nickname.
	other := newCard(t, "alice")
	f2, _, err := b.Add(other, "", 1)
	if err != nil || f2.Nickname != "alice-2" {
		t.Fatalf("duplicate nickname: %+v, %v", f2, err)
	}
	// Re-adding a known key refreshes addresses but keeps nickname and quota.
	alice.Addrs = []multiaddr.Multiaddr{multiaddr.StringCast("/ip4/198.51.100.7/tcp/4001")}
	f3, isNew, err := b.Add(alice, "", 5)
	if err != nil || isNew || f3.Give != 1<<30 || f3.Addrs[0] != "/ip4/198.51.100.7/tcp/4001" {
		t.Fatalf("refresh: %+v, %v, %v", f3, isNew, err)
	}
	// Explicitly taking a used nickname is an error.
	if _, _, err := b.Add(newCard(t, "x"), "alice", 1); err == nil {
		t.Fatal("taking a used nickname should fail")
	}

	b2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	aliceHex := hex.EncodeToString(alice.PubKey)
	pid, _ := PeerIDFromHex(aliceHex)
	for _, sel := range []string{"alice", aliceHex, aliceHex[:10], pid.String()} {
		got, err := b2.Resolve(sel)
		if err != nil || got.PubKey != aliceHex {
			t.Errorf("Resolve(%q) = %s, %v", sel, got.PubKey, err)
		}
	}
	if _, err := b2.Resolve("nobody"); err == nil {
		t.Fatal("Resolve(unknown) should fail")
	}
	if !b2.IsFriendPeer(pid) {
		t.Fatal("IsFriendPeer(alice) = false")
	}

	if err := b2.Update(aliceHex, func(f *Friend) error { f.Nickname = "alice-2"; return nil }); err == nil {
		t.Fatal("renaming onto a used nickname should fail")
	}
	if err := b2.Remove(aliceHex); err != nil || b2.IsFriend(aliceHex) {
		t.Fatalf("Remove: %v", err)
	}
}

func TestImportTrust(t *testing.T) {
	dir := t.TempDir()
	trusted := strings.Repeat("ab", 32)
	legacy := `{"version":1,"trusted":["` + strings.ToUpper(trusted) + `","zz"],"blocked":["` + strings.Repeat("cd", 32) + `"]}`
	if err := os.WriteFile(filepath.Join(dir, "trust.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := Load(filepath.Join(dir, "friends.json"))
	n, err := b.ImportTrust(filepath.Join(dir, "trust.json"), 42)
	if err != nil || n != 1 {
		t.Fatalf("ImportTrust = %d, %v; want 1", n, err)
	}
	f, ok := b.Get(trusted)
	if !ok || f.Give != 42 || f.Nickname != trusted[:8] {
		t.Fatalf("imported friend = %+v, %v", f, ok)
	}
	if !Exists(filepath.Join(dir, "friends.json")) {
		t.Fatal("import should persist the friend list")
	}
}

func TestRefreshSeesOtherWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "friends.json")
	daemon, _ := Load(path)
	cli, _ := Load(path)
	c := newCard(t, "carol")
	if _, _, err := cli.Add(c, "", 1); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(c.PubKey)
	if err := daemon.Refresh(); err != nil || !daemon.IsFriend(key) {
		t.Fatalf("Refresh did not pick up the new friend (err %v)", err)
	}
}

func TestApplyAddrs(t *testing.T) {
	b, _ := Load(filepath.Join(t.TempDir(), "friends.json"))
	c := newCard(t, "dave", "/ip4/192.0.2.1/tcp/4001")
	b.Add(c, "", 1)
	key := hex.EncodeToString(c.PubKey)
	if ok, err := b.ApplyAddrs(key, []string{"/ip4/192.0.2.2/tcp/4001"}, 10, []byte("sig")); !ok || err != nil {
		t.Fatalf("first signed record not applied: %v", err)
	}
	if ok, _ := b.ApplyAddrs(key, []string{"/ip4/192.0.2.3/tcp/4001"}, 5, []byte("old")); ok {
		t.Fatal("older record replaced a newer one")
	}
	if f, _ := b.Get(key); f.Addrs[0] != "/ip4/192.0.2.2/tcp/4001" || f.AddrSeq != 10 {
		t.Fatalf("friend = %+v", f)
	}
	if ok, _ := b.ApplyAddrs(strings.Repeat("ab", 32), []string{"/ip4/192.0.2.9/tcp/1"}, 99, nil); ok {
		t.Fatal("record applied for a non-friend")
	}
}

func TestInviteRoundTripAndConsume(t *testing.T) {
	dir := t.TempDir()
	iv := OpenInvites(filepath.Join(dir, "invites.json"))
	if iv.Pending() {
		t.Fatal("no invites yet")
	}
	token, exp, err := iv.Create("bob", 7, time.Hour)
	if err != nil || !iv.Pending() {
		t.Fatalf("create: %v", err)
	}
	c := newCard(t, "alice", "/ip4/192.0.2.1/tcp/4001")
	c.Token, c.Expires = token, exp
	got, err := DecodeCard(c.Encode())
	if err != nil || !got.IsInvite() || !bytes.Equal(got.Token, token) || !got.Expires.Equal(exp) {
		t.Fatalf("invite round trip: %+v, %v", got, err)
	}
	if plain, _ := DecodeCard(newCard(t, "x").Encode()); plain.IsInvite() {
		t.Fatal("plain card decoded as invite")
	}

	inv, ok := iv.Consume(token)
	if !ok || inv.Nickname != "bob" || inv.Give != 7 {
		t.Fatalf("consume = %+v, %v", inv, ok)
	}
	if _, ok := iv.Consume(token); ok {
		t.Fatal("token used twice")
	}
	if iv.Pending() {
		t.Fatal("consumed invite still pending")
	}
	expired, _, _ := iv.Create("", 1, -time.Second)
	if _, ok := iv.Consume(expired); ok {
		t.Fatal("expired token accepted")
	}
}
