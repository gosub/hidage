package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// Deterministic, content-addressed encryption for the versioned-storage core.
//
// Identical (masterKey, plaintext) must always produce identical ciphertext, so
// that an unchanged chunk hashes to the same store address and is never
// re-uploaded across snapshots. We achieve this without a random nonce by
// deriving everything from the plaintext under the repo's secret master key:
//
//	fp    = HMAC(masterKey, plaintext)            // the chunk fingerprint
//	key   = HMAC(masterKey, "…key…"  || fp)[:32]
//	nonce = HMAC(masterKey, "…nonce…"|| fp)[:12]
//
// fp is stored (inside encrypted DAG nodes) and, together with masterKey, is
// what OpenDeterministic needs. Because key/nonce derive from the secret master
// key, holding the ciphertext or fp alone does not allow decryption, and storing
// fp does not reveal the plaintext or leak equality to anyone without the key.

var (
	labelKey   = []byte("hidage-det-key-v1")
	labelNonce = []byte("hidage-det-nonce-v1")
)

func hmacSum(key, label, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(label)
	h.Write(data)
	return h.Sum(nil)
}

// SealDeterministic encrypts plaintext under masterKey and returns the
// ciphertext plus the fingerprint fp required to open it.
func SealDeterministic(masterKey, plaintext []byte) (ciphertext, fp []byte, err error) {
	fp = hmacSum(masterKey, nil, plaintext)
	key := hmacSum(masterKey, labelKey, fp)[:KeySize]
	nonce := hmacSum(masterKey, labelNonce, fp)[:chacha20poly1305.NonceSize]
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, plaintext, nil), fp, nil
}

// OpenDeterministic reverses SealDeterministic. It verifies, after decryption,
// that the recovered plaintext actually matches fp under masterKey, so a swapped
// (fp, ciphertext) pairing is rejected.
func OpenDeterministic(masterKey, fp, ciphertext []byte) ([]byte, error) {
	key := hmacSum(masterKey, labelKey, fp)[:KeySize]
	nonce := hmacSum(masterKey, labelNonce, fp)[:chacha20poly1305.NonceSize]
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("deterministic open failed (corrupt or wrong key/fp)")
	}
	if !hmac.Equal(hmacSum(masterKey, nil, plaintext), fp) {
		return nil, errors.New("deterministic open: fingerprint mismatch")
	}
	return plaintext, nil
}
