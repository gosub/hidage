package trust

import (
	"crypto/ed25519"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, hex.EncodeToString(pub)
}

func newPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := Load(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerdictDefaultsNeutral(t *testing.T) {
	p := newPolicy(t)
	pub, _ := newKey(t)
	if v := p.Verdict(pub); v != Neutral {
		t.Fatalf("unknown key should be Neutral, got %v", v)
	}
}

func TestTrustAndBlockVerdicts(t *testing.T) {
	p := newPolicy(t)
	trustedPub, trustedHex := newKey(t)
	blockedPub, blockedHex := newKey(t)

	if err := p.Trust(trustedHex); err != nil {
		t.Fatal(err)
	}
	if err := p.Block(blockedHex); err != nil {
		t.Fatal(err)
	}
	if v := p.Verdict(trustedPub); v != Trusted {
		t.Fatalf("want Trusted, got %v", v)
	}
	if v := p.Verdict(blockedPub); v != Blocked {
		t.Fatalf("want Blocked, got %v", v)
	}
}

func TestMutualExclusion(t *testing.T) {
	p := newPolicy(t)
	pub, key := newKey(t)

	// Trust then block: block wins and trust is cleared.
	p.Trust(key)
	p.Block(key)
	if v := p.Verdict(pub); v != Blocked {
		t.Fatalf("after block, want Blocked, got %v", v)
	}
	trusted, blocked := p.List()
	if len(trusted) != 0 || len(blocked) != 1 {
		t.Fatalf("expected only blocked, got trusted=%v blocked=%v", trusted, blocked)
	}

	// Trust again: clears the block.
	p.Trust(key)
	if v := p.Verdict(pub); v != Trusted {
		t.Fatalf("after re-trust, want Trusted, got %v", v)
	}
}

func TestRemoveAndUnblock(t *testing.T) {
	p := newPolicy(t)
	pub, key := newKey(t)
	p.Trust(key)
	p.Remove(key)
	if v := p.Verdict(pub); v != Neutral {
		t.Fatalf("after remove, want Neutral, got %v", v)
	}
	p.Block(key)
	p.Unblock(key)
	if v := p.Verdict(pub); v != Neutral {
		t.Fatalf("after unblock, want Neutral, got %v", v)
	}
}

func TestValidationRejectsBadKeys(t *testing.T) {
	p := newPolicy(t)
	if err := p.Trust("nothex"); err == nil {
		t.Fatal("expected non-hex key to fail")
	}
	if err := p.Trust("abcd"); err == nil {
		t.Fatal("expected wrong-length key to fail")
	}
}

func TestPersistenceRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	p, _ := Load(path)
	_, tk := newKey(t)
	_, bk := newKey(t)
	p.Trust(tk)
	p.Block(bk)

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	trusted, blocked := reloaded.List()
	if len(trusted) != 1 || trusted[0] != tk {
		t.Fatalf("trusted not persisted: %v", trusted)
	}
	if len(blocked) != 1 || blocked[0] != bk {
		t.Fatalf("blocked not persisted: %v", blocked)
	}
}
