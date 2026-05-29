// Package identity holds a node's Ed25519 signing keypair. The public key is the
// node's pseudonymous identity (and the future libp2p peer key); the private key
// signs snapshots. The keypair is derived from the user keyring's root seed (see
// internal/keyring), not stored on its own.
package identity

import "crypto/ed25519"

// Identity is a node's signing keypair.
type Identity struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// FromSeed derives an identity from a 32-byte Ed25519 seed.
func FromSeed(seed []byte) Identity {
	priv := ed25519.NewKeyFromSeed(seed)
	return Identity{Public: priv.Public().(ed25519.PublicKey), Private: priv}
}

// Generate creates a fresh random identity (used in tests).
func Generate() (Identity, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Public: pub, Private: priv}, nil
}
