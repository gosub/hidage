package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeyedStore is a blob store addressed by an externally-supplied id rather than
// by the hash of its contents. It backs a node's set of published/served network
// objects, where ids are a mix of chunk/node content addresses and snapshot ids
// (whose id is a hash of the signed payload, not of the stored bytes). Callers
// verify integrity on read (content addresses by re-hashing, snapshots by
// signature), so the store itself need not be content-addressed.
type KeyedStore struct {
	root string
}

// NewKeyedStore opens (creating if needed) a keyed store rooted at root.
func NewKeyedStore(root string) (*KeyedStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create keyed store: %w", err)
	}
	return &KeyedStore{root: root}, nil
}

func (s *KeyedStore) path(id string) string {
	if len(id) < 2 {
		return filepath.Join(s.root, "_", id)
	}
	return filepath.Join(s.root, id[:2], id)
}

// Put stores data under id (idempotent).
func (s *KeyedStore) Put(id string, data []byte) error {
	p := s.path(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Get returns the bytes stored under id, or ErrNotFound.
func (s *KeyedStore) Get(id string) ([]byte, error) {
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Has reports whether id is present.
func (s *KeyedStore) Has(id string) (bool, error) {
	_, err := os.Stat(s.path(id))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// List returns all stored ids.
func (s *KeyedStore) List() ([]string, error) {
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
	return ids, err
}
