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
