// Package crypto handles client-side key material for hidage. A key-encryption
// key (KEK) is derived from the user's passphrase via Argon2id and used to wrap
// (seal) the 32-byte root secret with ChaCha20-Poly1305; only the wrapped root
// and the Argon2id parameters are written to the keyring, so a stolen keyring
// file is useless without the passphrase.
//
// Repository objects (chunks and DAG nodes) are not sealed here: they use the
// deterministic, content-addressed scheme in deterministic.go, which derives the
// key and nonce from the plaintext under the user's master key so identical
// content deduplicates. This file is only the passphrase→root wrapping.
package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the symmetric key length (ChaCha20-Poly1305 / Argon2 output).
const KeySize = chacha20poly1305.KeySize

// Argon2Params records the Argon2id cost parameters and salt used to derive a
// KEK. Stored in the manifest so restore can reproduce the derivation.
type Argon2Params struct {
	Salt    []byte `json:"salt"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"keyLen"`
}

// DefaultArgon2Params returns interactive-grade Argon2id parameters with a
// fresh random salt.
func DefaultArgon2Params() (Argon2Params, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Argon2Params{}, fmt.Errorf("generate salt: %w", err)
	}
	return Argon2Params{
		Salt:    salt,
		Time:    3,
		Memory:  64 * 1024, // 64 MiB
		Threads: 4,
		KeyLen:  KeySize,
	}, nil
}

// DeriveKEK derives a key-encryption key from a passphrase and parameters.
func DeriveKEK(passphrase string, p Argon2Params) []byte {
	return argon2.IDKey([]byte(passphrase), p.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
}

// NewDEK generates a fresh random data-encryption key.
func NewDEK() ([]byte, error) {
	dek := make([]byte, KeySize)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("generate DEK: %w", err)
	}
	return dek, nil
}

// WrapDEK seals the DEK under the KEK using a single AEAD with a random nonce.
// Output layout: nonce || ciphertext.
func WrapDEK(kek, dek []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate wrap nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, dek, nil), nil
}

// UnwrapDEK reverses WrapDEK. A wrong passphrase fails the AEAD tag check.
func UnwrapDEK(kek, wrapped []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(kek)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(wrapped) < ns {
		return nil, errors.New("wrapped DEK too short")
	}
	nonce, ct := wrapped[:ns], wrapped[ns:]
	dek, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("unwrap DEK failed (wrong passphrase?)")
	}
	return dek, nil
}
