package crypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// SubKey derives a purpose-specific key from the master key (HKDF-SHA256 with
// label), so small private records (e.g. the friend roster in the account
// manifest) are never encrypted directly under the key that seals objects.
func SubKey(masterKey []byte, label string) []byte {
	k, err := hkdf.Key(sha256.New, masterKey, nil, label, KeySize)
	if err != nil {
		panic(fmt.Sprintf("hkdf derive: %v", err)) // sha256 + KeySize cannot fail
	}
	return k
}

// Seal encrypts a small record under key with a random nonce (nonce ||
// ciphertext). Unlike SealDeterministic, equal plaintexts give different
// ciphertexts.
func Seal(key, plaintext []byte) ([]byte, error) { return WrapDEK(key, plaintext) }

// Open reverses Seal.
func Open(key, sealed []byte) ([]byte, error) { return UnwrapDEK(key, sealed) }
