// Package crypto handles client-side encryption for hidage. Plaintext is
// sealed with ChaCha20-Poly1305 under a random per-backup data-encryption key
// (DEK); the DEK is itself wrapped under a key-encryption key (KEK) derived
// from the user's passphrase via Argon2id. Only the wrapped DEK and the
// Argon2id parameters travel in the manifest, so hosts holding shards learn
// nothing about the content.
package crypto

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// DefaultChunkSize is the plaintext chunk size for the streaming AEAD. Each
// chunk is sealed independently so memory stays bounded and corruption is
// localized.
const DefaultChunkSize = 64 * 1024

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

// chunkNonce builds the per-chunk nonce: 8-byte big-endian counter in the high
// bytes, with the final byte set to 1 on the last chunk. The final-chunk flag
// is what gives truncation protection: a stream missing its tail will not
// present a chunk carrying the flag, so decryption fails.
func chunkNonce(counter uint64, last bool) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSize)
	binary.BigEndian.PutUint64(nonce[0:8], counter)
	if last {
		nonce[chacha20poly1305.NonceSize-1] = 1
	}
	return nonce
}

// Encrypt seals plaintext under dek as a sequence of AEAD chunks. Each non-final
// chunk produces exactly chunkSize+Overhead ciphertext bytes; the final chunk
// holds the remainder and is nonce-flagged.
func Encrypt(dek, plaintext []byte, chunkSize int) ([]byte, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	aead, err := chacha20poly1305.New(dek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(plaintext)+aead.Overhead()*(len(plaintext)/chunkSize+1))
	var counter uint64
	for offset := 0; ; offset += chunkSize {
		end := offset + chunkSize
		last := end >= len(plaintext)
		if last {
			end = len(plaintext)
		}
		nonce := chunkNonce(counter, last)
		out = aead.Seal(out, nonce, plaintext[offset:end], nil)
		counter++
		if last {
			break
		}
	}
	return out, nil
}

// Decrypt reverses Encrypt. It rejects a stream whose final chunk is missing or
// whose chunks have been reordered, truncated, or corrupted.
func Decrypt(dek, ciphertext []byte, chunkSize int) ([]byte, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	aead, err := chacha20poly1305.New(dek)
	if err != nil {
		return nil, err
	}
	sealedChunk := chunkSize + aead.Overhead()
	out := make([]byte, 0, len(ciphertext))
	var counter uint64
	for len(ciphertext) > 0 {
		n := sealedChunk
		last := false
		if len(ciphertext) <= sealedChunk {
			n = len(ciphertext)
			last = true
		}
		nonce := chunkNonce(counter, last)
		var openErr error
		out, openErr = aead.Open(out, nonce, ciphertext[:n], nil)
		if openErr != nil {
			return nil, errors.New("decrypt failed (corrupt, truncated, or wrong key)")
		}
		ciphertext = ciphertext[n:]
		counter++
	}
	return out, nil
}
