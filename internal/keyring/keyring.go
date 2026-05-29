// Package keyring manages the user's single root secret, from which everything
// else is derived. The 32-byte root is generated at `hidage init`, wrapped under
// a passphrase-derived KEK, and stored at ~/.config/hidage/keyring.json. The
// master encryption key and the Ed25519 identity are both HKDF-derived from the
// root with distinct labels, so one secret backs up the whole node — and the
// root can be exported as a single BIP39 recovery mnemonic.
package keyring

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	bip39 "github.com/tyler-smith/go-bip39"

	"hidage/internal/crypto"
	"hidage/internal/identity"
)

// rootSize is the root secret length; 32 bytes encodes to a 24-word BIP39 phrase.
const rootSize = 32

const (
	labelMasterKey = "hidage/master-key/v1"
	labelIdentity  = "hidage/identity/v1"
)

// wrapped is the on-disk form: Argon2id parameters plus the root sealed under the
// passphrase KEK. The raw root is never written.
type wrapped struct {
	Version     int                 `json:"version"`
	Argon2      crypto.Argon2Params `json:"argon2"`
	WrappedRoot []byte              `json:"wrappedRoot"`
}

// Keyring is an unlocked root secret with its derived keys.
type Keyring struct {
	root []byte
}

func derive(root []byte, label string, n int) []byte {
	out, err := hkdf.Key(sha256.New, root, nil, label, n)
	if err != nil {
		panic(fmt.Sprintf("hkdf derive: %v", err)) // sha256 + small n cannot fail
	}
	return out
}

// MasterKey returns the symmetric key used to encrypt all repository objects.
func (k *Keyring) MasterKey() []byte {
	return derive(k.root, labelMasterKey, crypto.KeySize)
}

// Identity returns the node's signing keypair.
func (k *Keyring) Identity() identity.Identity {
	return identity.FromSeed(derive(k.root, labelIdentity, ed25519.SeedSize))
}

// RecoveryCode returns the BIP39 mnemonic encoding the root secret. Anyone with
// this phrase has full control of the keyring, so it must be guarded like the
// passphrase itself.
func (k *Keyring) RecoveryCode() (string, error) {
	return bip39.NewMnemonic(k.root)
}

// Exists reports whether a keyring is present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Init generates a new root secret, wraps it under the passphrase, and writes it.
func Init(path, passphrase string) (*Keyring, error) {
	if Exists(path) {
		return nil, fmt.Errorf("keyring already exists at %s", path)
	}
	root := make([]byte, rootSize)
	if _, err := rand.Read(root); err != nil {
		return nil, err
	}
	if err := persist(path, root, passphrase); err != nil {
		return nil, err
	}
	return &Keyring{root: root}, nil
}

// Load reads and unlocks the keyring with the passphrase.
func Load(path, passphrase string) (*Keyring, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read keyring: %w", err)
	}
	var w wrapped
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse keyring: %w", err)
	}
	kek := crypto.DeriveKEK(passphrase, w.Argon2)
	root, err := crypto.UnwrapDEK(kek, w.WrappedRoot)
	if err != nil {
		return nil, errors.New("cannot unlock keyring (wrong passphrase?)")
	}
	return &Keyring{root: root}, nil
}

// Restore writes a new keyring from a BIP39 recovery mnemonic, wrapped under a
// fresh passphrase. It refuses to overwrite an existing keyring.
func Restore(path, mnemonic, passphrase string) (*Keyring, error) {
	if Exists(path) {
		return nil, fmt.Errorf("keyring already exists at %s (remove it before recovering)", path)
	}
	root, err := bip39.EntropyFromMnemonic(strings.TrimSpace(mnemonic))
	if err != nil {
		return nil, fmt.Errorf("invalid recovery code: %w", err)
	}
	if len(root) != rootSize {
		return nil, fmt.Errorf("recovery code must encode %d bytes, got %d", rootSize, len(root))
	}
	if err := persist(path, root, passphrase); err != nil {
		return nil, err
	}
	return &Keyring{root: root}, nil
}

func persist(path string, root []byte, passphrase string) error {
	argon, err := crypto.DefaultArgon2Params()
	if err != nil {
		return err
	}
	kek := crypto.DeriveKEK(passphrase, argon)
	wr, err := crypto.WrapDEK(kek, root)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(wrapped{Version: 1, Argon2: argon, WrappedRoot: wr}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
