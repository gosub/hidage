// Package userkey manages the user-level encryption master key. A single master
// key per user (not per repository) is generated at `hidage init`, wrapped under
// a passphrase-derived KEK, and stored in the user config directory. All
// repositories encrypt with this one key, which keeps key management simple and
// lets identical chunks deduplicate across every folder you back up.
package userkey

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"hidage/internal/crypto"
)

// wrapped is the on-disk form: the Argon2id parameters plus the master key
// sealed under the passphrase KEK. The raw key is never written.
type wrapped struct {
	Version    int                 `json:"version"`
	Argon2     crypto.Argon2Params `json:"argon2"`
	WrappedKey []byte              `json:"wrappedKey"`
}

// Exists reports whether a master key file is present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Init generates a fresh master key, wraps it under the passphrase, and writes
// it to path. It returns the unwrapped key for immediate use. Refuses to
// overwrite an existing key.
func Init(path, passphrase string) ([]byte, error) {
	if Exists(path) {
		return nil, fmt.Errorf("master key already exists at %s", path)
	}
	argon, err := crypto.DefaultArgon2Params()
	if err != nil {
		return nil, err
	}
	mk, err := crypto.NewDEK()
	if err != nil {
		return nil, err
	}
	kek := crypto.DeriveKEK(passphrase, argon)
	wk, err := crypto.WrapDEK(kek, mk)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(wrapped{Version: 1, Argon2: argon, WrappedKey: wk}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return mk, nil
}

// Load reads and unwraps the master key with the passphrase.
func Load(path, passphrase string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	var w wrapped
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse master key: %w", err)
	}
	kek := crypto.DeriveKEK(passphrase, w.Argon2)
	mk, err := crypto.UnwrapDEK(kek, w.WrappedKey)
	if err != nil {
		return nil, errors.New("cannot unlock master key (wrong passphrase?)")
	}
	return mk, nil
}
