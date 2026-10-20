package friends

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	last := s[len(s)-1]
	flip := byte('a')
	if last == 'a' {
		flip = 'b'
	}
	for _, bad := range []string{s[:len(s)-1] + string(flip), s[:30], "hello", strings.TrimPrefix(s, CardPrefix)} {
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
