package userkey

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestInitLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "masterkey.json")
	mk, err := Init(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, mk) {
		t.Fatal("loaded master key does not match the one created")
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "masterkey.json")
	if _, err := Init(path, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "wrong"); err == nil {
		t.Fatal("expected Load with wrong passphrase to fail")
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "masterkey.json")
	if _, err := Init(path, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(path, "p"); err == nil {
		t.Fatal("expected Init to refuse overwriting an existing key")
	}
}
