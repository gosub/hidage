package crypto

import (
	"bytes"
	"testing"
)

func TestSealDeterministicIsDeterministic(t *testing.T) {
	mk, _ := NewDEK()
	plain := []byte("the same plaintext, every time")

	ct1, fp1, err := SealDeterministic(mk, plain)
	if err != nil {
		t.Fatal(err)
	}
	ct2, fp2, err := SealDeterministic(mk, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ct1, ct2) {
		t.Fatal("ciphertext is not deterministic")
	}
	if !bytes.Equal(fp1, fp2) {
		t.Fatal("fingerprint is not deterministic")
	}
}

func TestSealOpenRoundtrip(t *testing.T) {
	mk, _ := NewDEK()
	plain := []byte("recover me exactly")
	ct, fp, err := SealDeterministic(mk, plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenDeterministic(mk, fp, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("roundtrip mismatch")
	}
}

func TestDifferentKeysDifferentCiphertext(t *testing.T) {
	plain := []byte("same content")
	mk1, _ := NewDEK()
	mk2, _ := NewDEK()
	ct1, _, _ := SealDeterministic(mk1, plain)
	ct2, _, _ := SealDeterministic(mk2, plain)
	if bytes.Equal(ct1, ct2) {
		t.Fatal("different master keys should not produce identical ciphertext")
	}
}

func TestOpenRejectsWrongFpOrCorruption(t *testing.T) {
	mk, _ := NewDEK()
	ct, fp, _ := SealDeterministic(mk, []byte("data"))

	badFp := append([]byte{}, fp...)
	badFp[0] ^= 0xFF
	if _, err := OpenDeterministic(mk, badFp, ct); err == nil {
		t.Fatal("expected open with wrong fp to fail")
	}

	badCt := append([]byte{}, ct...)
	badCt[0] ^= 0xFF
	if _, err := OpenDeterministic(mk, fp, badCt); err == nil {
		t.Fatal("expected open of corrupted ciphertext to fail")
	}
}
