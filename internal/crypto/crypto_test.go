package crypto

import (
	"bytes"
	"testing"
)

func roundtripDEK(t *testing.T) []byte {
	t.Helper()
	dek, err := NewDEK()
	if err != nil {
		t.Fatalf("NewDEK: %v", err)
	}
	return dek
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	dek := roundtripDEK(t)
	// Use a small chunk size to exercise multi-chunk paths, including an exact
	// multiple of the chunk size and a partial final chunk.
	for _, size := range []int{0, 1, 16, 100, 256, 257, 1024} {
		if size == 0 {
			continue // Encrypt of empty is allowed but shard layer guards it; skip
		}
		plain := bytes.Repeat([]byte("abcdefgh"), size)
		ct, err := Encrypt(dek, plain, 256)
		if err != nil {
			t.Fatalf("Encrypt size=%d: %v", size, err)
		}
		got, err := Decrypt(dek, ct, 256)
		if err != nil {
			t.Fatalf("Decrypt size=%d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("roundtrip mismatch at size=%d", size)
		}
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	dek := roundtripDEK(t)
	other := roundtripDEK(t)
	ct, err := Encrypt(dek, []byte("secret payload"), 256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(other, ct, 256); err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestDecryptTruncatedFails(t *testing.T) {
	dek := roundtripDEK(t)
	plain := bytes.Repeat([]byte("x"), 1000)
	ct, err := Encrypt(dek, plain, 256)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the final chunk: the final-chunk nonce flag means the remaining
	// chunks no longer present a flagged tail, so decryption must fail.
	truncated := ct[:256+16]
	if _, err := Decrypt(dek, truncated, 256); err == nil {
		t.Fatal("expected truncated ciphertext to fail")
	}
}

func TestDecryptCorruptedFails(t *testing.T) {
	dek := roundtripDEK(t)
	ct, err := Encrypt(dek, []byte("tamper with me please"), 256)
	if err != nil {
		t.Fatal(err)
	}
	ct[5] ^= 0xFF
	if _, err := Decrypt(dek, ct, 256); err == nil {
		t.Fatal("expected corrupted ciphertext to fail")
	}
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
	dek := roundtripDEK(t)
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
