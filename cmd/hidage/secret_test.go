package main

import (
	"errors"
	"path/filepath"
	"testing"

	secrets "github.com/zalando/go-keyring"

	"hidage/internal/keyring"
)

func TestSavedPassphraseUnlocks(t *testing.T) {
	secrets.MockInit()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "mock")
	t.Setenv(passphraseEnv, "")
	path := filepath.Join(t.TempDir(), "keyring.json")
	if _, err := keyring.Init(path, "right"); err != nil {
		t.Fatal(err)
	}

	if err := savePassphrase("right"); err != nil {
		t.Fatal(err)
	}
	if _, err := unlockKeyring(path); err != nil {
		t.Fatalf("saved passphrase did not unlock: %v", err)
	}

	// A wrong saved passphrase is removed, then the (missing) terminal asked.
	savePassphrase("wrong")
	_, err := unlockKeyring(path)
	if !errors.As(err, new(errNoPassphrase)) {
		t.Fatalf("err = %v, want errNoPassphrase (no terminal in tests)", err)
	}
	if _, ok := savedPassphrase(); ok {
		t.Fatal("the wrong passphrase is still saved")
	}
}
