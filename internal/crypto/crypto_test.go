package crypto

import (
	"bytes"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	dek, err := NewDEK()
	if err != nil {
		t.Fatalf("NewDEK: %v", err)
	}
	return dek
}

func TestWrapUnwrapDEK(t *testing.T) {
	params, err := DefaultArgon2Params()
	if err != nil {
		t.Fatal(err)
	}
	// Cheaper params for the test.
	params.Time = 1
	params.Memory = 8 * 1024
	kek := DeriveKEK("correct horse", params)
	dek := newKey(t)
	wrapped, err := WrapDEK(kek, dek)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapDEK(kek, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrapped DEK mismatch")
	}
	wrongKEK := DeriveKEK("battery staple", params)
	if _, err := UnwrapDEK(wrongKEK, wrapped); err == nil {
		t.Fatal("expected unwrap with wrong passphrase to fail")
	}
}

func TestSealOpenSubKey(t *testing.T) {
	master := make([]byte, KeySize)
	k1, k2 := SubKey(master, "a"), SubKey(master, "b")
	if string(k1) == string(k2) || string(k1) == string(master) {
		t.Fatal("subkeys must differ by label and from the master key")
	}
	s1, err := Seal(k1, []byte("roster"))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := Seal(k1, []byte("roster"))
	if string(s1) == string(s2) {
		t.Fatal("Seal must use a random nonce")
	}
	if pt, err := Open(k1, s1); err != nil || string(pt) != "roster" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	if _, err := Open(k2, s1); err == nil {
		t.Fatal("wrong key opened the record")
	}
}
