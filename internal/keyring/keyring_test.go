package keyring

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestInitDeriveDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	kr, err := Init(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	// Re-loading derives the same master key and identity.
	kr2, err := Load(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kr.MasterKey(), kr2.MasterKey()) {
		t.Fatal("master key not stable across load")
	}
	if !bytes.Equal(kr.Identity().Public, kr2.Identity().Public) {
		t.Fatal("identity not stable across load")
	}
	// Master key and identity seed must differ (distinct HKDF labels).
	if bytes.Equal(kr.MasterKey(), kr.Identity().Private.Seed()) {
		t.Fatal("master key and identity seed should not be identical")
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if _, err := Init(path, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "wrong"); err == nil {
		t.Fatal("expected wrong passphrase to fail")
	}
}

func TestRecoveryCodeRoundtrip(t *testing.T) {
	p1 := filepath.Join(t.TempDir(), "keyring.json")
	kr, err := Init(p1, "pass-a")
	if err != nil {
		t.Fatal(err)
	}
	code, err := kr.RecoveryCode()
	if err != nil {
		t.Fatal(err)
	}

	// Restore on a "fresh machine" with a different passphrase.
	p2 := filepath.Join(t.TempDir(), "keyring.json")
	restored, err := Restore(p2, code, "pass-b")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kr.MasterKey(), restored.MasterKey()) {
		t.Fatal("recovered master key differs from original")
	}
	if !bytes.Equal(kr.Identity().Public, restored.Identity().Public) {
		t.Fatal("recovered identity differs from original")
	}
	// And it can be reopened with the new passphrase.
	reopened, err := Load(p2, "pass-b")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reopened.MasterKey(), kr.MasterKey()) {
		t.Fatal("reopened master key differs")
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if _, err := Restore(path, "not a valid mnemonic at all", "p"); err == nil {
		t.Fatal("expected invalid recovery code to fail")
	}
}
