package repo

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hidage/internal/dag"
)

// Snapshot is a signed root of one version of the source tree. Snapshots form a
// chain via Parent, giving a git-like history.
type Snapshot struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"` // hex SHA-256 of the signed payload
	CreatedAt   time.Time `json:"createdAt"`
	Parent      string    `json:"parent"` // previous snapshot ID, or ""
	Source      string    `json:"source"` // base name of the source, for display
	Root        dag.Ref   `json:"root"`   // root tree node
	OwnerPubKey []byte    `json:"ownerPubKey"`
	Signature   []byte    `json:"signature"`
}

// signingBytes is the canonical payload signed and hashed: everything except the
// derived ID and the Signature itself.
func (s *Snapshot) signingBytes() ([]byte, error) {
	c := *s
	c.ID = ""
	c.Signature = nil
	return json.Marshal(c)
}

// Sign attaches the owner's signature and computes the content-addressed ID.
func (s *Snapshot) Sign(priv ed25519.PrivateKey) error {
	s.OwnerPubKey = priv.Public().(ed25519.PublicKey)
	payload, err := s.signingBytes()
	if err != nil {
		return err
	}
	s.Signature = ed25519.Sign(priv, payload)
	sum := sha256.Sum256(payload)
	s.ID = hex.EncodeToString(sum[:])
	return nil
}

// Verify checks the signature and that the ID matches the payload.
func (s *Snapshot) Verify() error {
	if len(s.OwnerPubKey) != ed25519.PublicKeySize {
		return errors.New("missing or malformed owner public key")
	}
	if len(s.Signature) == 0 {
		return errors.New("snapshot is unsigned")
	}
	payload, err := s.signingBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(s.OwnerPubKey), payload, s.Signature) {
		return errors.New("snapshot signature verification failed")
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != s.ID {
		return errors.New("snapshot ID does not match content")
	}
	return nil
}

func (r *Repo) snapshotPath(id string) string {
	return filepath.Join(snapshotsDir(r.Path), id+".json")
}

func (r *Repo) saveSnapshot(s *Snapshot) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.snapshotPath(s.ID), data, 0o600)
}

// ListSnapshots returns all snapshots, verified, oldest first.
func (r *Repo) ListSnapshots() ([]*Snapshot, error) {
	entries, err := os.ReadDir(snapshotsDir(r.Path))
	if err != nil {
		return nil, fmt.Errorf("read snapshots dir: %w", err)
	}
	var snaps []*Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(snapshotsDir(r.Path), e.Name()))
		if err != nil {
			return nil, err
		}
		var s Snapshot
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("parse snapshot %s: %w", e.Name(), err)
		}
		if err := s.Verify(); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", s.ID, err)
		}
		snaps = append(snaps, &s)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].CreatedAt.Before(snaps[j].CreatedAt) })
	return snaps, nil
}

// FindSnapshot resolves a full ID or unambiguous prefix to one snapshot.
func (r *Repo) FindSnapshot(idOrPrefix string) (*Snapshot, error) {
	snaps, err := r.ListSnapshots()
	if err != nil {
		return nil, err
	}
	var matches []*Snapshot
	for _, s := range snaps {
		if strings.HasPrefix(s.ID, idOrPrefix) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no snapshot matching %q", idOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous snapshot prefix %q (%d matches)", idOrPrefix, len(matches))
	}
}

// latest returns the most recent snapshot, or nil if none exist.
func (r *Repo) latest() (*Snapshot, error) {
	snaps, err := r.ListSnapshots()
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, nil
	}
	return snaps[len(snaps)-1], nil
}
