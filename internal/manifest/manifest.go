// Package manifest defines the recovery recipe for a backup: the metadata,
// crypto parameters, fountain-code parameters, and the list of shard
// references needed to reconstruct a file or folder. A manifest is signed by
// the owner's Ed25519 identity so authorship can be verified, and it is the
// one artifact the owner must keep to restore.
package manifest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"hidage/internal/crypto"
	"hidage/internal/shard"
)

// Version is the current manifest schema version.
const Version = 1

// CryptoParams records how the blob was encrypted.
type CryptoParams struct {
	Algorithm  string              `json:"algorithm"`
	Argon2     crypto.Argon2Params `json:"argon2"`
	WrappedDEK []byte              `json:"wrappedDEK"`
	ChunkSize  int                 `json:"chunkSize"`
}

// ShardRef points at one shard in the store and carries the BlockCode needed to
// decode it.
type ShardRef struct {
	ID        string `json:"id"` // content address (hex SHA-256) in the store
	BlockCode int64  `json:"blockCode"`
	Size      int    `json:"size"`
}

// Manifest is the full recovery recipe.
type Manifest struct {
	Version         int          `json:"version"`
	CreatedAt       time.Time    `json:"createdAt"`
	OwnerPubKey     []byte       `json:"ownerPubKey"`
	OriginalName    string       `json:"originalName"`
	IsArchive       bool         `json:"isArchive"`
	PlaintextSize   int64        `json:"plaintextSize"`
	PlaintextSHA256 []byte       `json:"plaintextSHA256"`
	Crypto          CryptoParams `json:"crypto"`
	Fountain        shard.Params `json:"fountain"`
	Shards          []ShardRef   `json:"shards"`
	Signature       []byte       `json:"signature"`
}

// signingBytes returns the deterministic serialization signed over: the whole
// manifest with the Signature field cleared. Go marshals struct fields in
// declaration order, so this is stable.
func (m *Manifest) signingBytes() ([]byte, error) {
	clone := *m
	clone.Signature = nil
	return json.Marshal(clone)
}

// Sign computes and attaches the Ed25519 signature using priv. It also records
// the corresponding public key.
func (m *Manifest) Sign(priv ed25519.PrivateKey) error {
	m.OwnerPubKey = priv.Public().(ed25519.PublicKey)
	msg, err := m.signingBytes()
	if err != nil {
		return fmt.Errorf("serialize for signing: %w", err)
	}
	m.Signature = ed25519.Sign(priv, msg)
	return nil
}

// Verify checks the signature against the embedded public key.
func (m *Manifest) Verify() error {
	if len(m.OwnerPubKey) != ed25519.PublicKeySize {
		return errors.New("missing or malformed owner public key")
	}
	if len(m.Signature) == 0 {
		return errors.New("manifest is unsigned")
	}
	msg, err := m.signingBytes()
	if err != nil {
		return fmt.Errorf("serialize for verify: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(m.OwnerPubKey), msg, m.Signature) {
		return errors.New("manifest signature verification failed")
	}
	return nil
}

// PlaintextHash is a convenience helper for callers computing the digest stored
// in PlaintextSHA256.
func PlaintextHash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// Save writes the manifest to path as indented JSON.
func (m *Manifest) Save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// Load reads a manifest from path.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}
