// Package identity manages a node's pseudonymous Ed25519 keypair. The public
// key is the node's network identity (and the future libp2p peer key); the
// private key signs manifests so an owner can prove authorship of a backup.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// Identity is a node's signing keypair.
type Identity struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// Generate creates a fresh random Ed25519 identity.
func Generate() (Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return Identity{Public: pub, Private: priv}, nil
}

// Save persists the identity to path. Only the 32-byte seed is written
// (base64-encoded) with owner-only permissions; the public key is derived on
// load. Refuses to clobber an existing key file.
func (id Identity) Save(path string) error {
	seed := id.Private.Seed()
	enc := base64.StdEncoding.EncodeToString(seed) + "\n"
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		return fmt.Errorf("write identity: %w", err)
	}
	return nil
}

// Load reads an identity previously written by Save.
func Load(path string) (Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, fmt.Errorf("read identity: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return Identity{}, fmt.Errorf("decode identity: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return Identity{}, fmt.Errorf("invalid identity seed length %d", len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return Identity{Public: pub, Private: priv}, nil
}

// Exists reports whether an identity file is present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
