// Package store persists shards by content address. The MVP ships a local
// filesystem implementation, but everything is expressed against the Store
// interface so a libp2p-backed network store can drop in during Phase 2
// without touching callers.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Get/Delete when no shard with the given ID exists.
var ErrNotFound = errors.New("shard not found")

// Store is a content-addressed blob store keyed by the hex SHA-256 of contents.
type Store interface {
	// Put stores data and returns its content address (hex SHA-256). Storing
	// the same bytes twice is idempotent.
	Put(data []byte) (id string, err error)
	// Get returns the bytes for id, or ErrNotFound.
	Get(id string) ([]byte, error)
	// Has reports whether id is present.
	Has(id string) (bool, error)
	// Delete removes id. Returns ErrNotFound if absent.
	Delete(id string) error
	// List returns all stored content addresses.
	List() ([]string, error)
}

// ID returns the content address (hex SHA-256) of data.
func ID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FSStore is a filesystem-backed Store. Blobs live at root/<id[:2]>/<id> to
// keep directory fan-out reasonable.
type FSStore struct {
	root string
}

// NewFSStore opens (creating if needed) a store rooted at root.
func NewFSStore(root string) (*FSStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create store root: %w", err)
	}
	return &FSStore{root: root}, nil
}

func (s *FSStore) path(id string) string {
	return filepath.Join(s.root, id[:2], id)
}

// Put implements Store.
func (s *FSStore) Put(data []byte) (string, error) {
	id := ID(data)
	p := s.path(id)
	if _, err := os.Stat(p); err == nil {
		return id, nil // already present
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", fmt.Errorf("create shard dir: %w", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", fmt.Errorf("write shard: %w", err)
	}
	return id, nil
}

// Get implements Store.
func (s *FSStore) Get(id string) ([]byte, error) {
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read shard %s: %w", id, err)
	}
	return data, nil
}

// Has implements Store.
func (s *FSStore) Has(id string) (bool, error) {
	_, err := os.Stat(s.path(id))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Delete implements Store.
func (s *FSStore) Delete(id string) error {
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// List implements Store.
func (s *FSStore) List() ([]string, error) {
	var ids []string
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			ids = append(ids, d.Name())
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list store: %w", err)
	}
	return ids, nil
}
