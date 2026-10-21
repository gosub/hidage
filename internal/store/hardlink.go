package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"hidage/internal/fsx"
)

// HardlinkStore is a content-addressed store that deduplicates blobs across
// repositories using hard links. One canonical copy of each blob lives in a
// shared pool; every repository gets its own hard link to it under a private
// namespace (refs/<namespace>/). The filesystem's link count acts as a
// cross-repository reference count, so a repository can prune its own links
// safely: a blob's disk space is reclaimed only when the last repository
// referencing it unlinks. The pool and the namespaces must live on one
// filesystem that supports hard links (they can't span filesystems).
type HardlinkStore struct {
	pool string // <root>/pool
	refs string // <root>/refs/<namespace>
}

// NewHardlinkStore opens a store under root for namespace (the repository ID).
func NewHardlinkStore(root, namespace string) (*HardlinkStore, error) {
	if err := fsx.MkdirPrivate(root); err != nil {
		return nil, fmt.Errorf("create %s: %w", root, err)
	}
	s := &HardlinkStore{
		pool: filepath.Join(root, "pool"),
		refs: filepath.Join(root, "refs", namespace),
	}
	for _, d := range []string{s.pool, s.refs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return s, nil
}

func (s *HardlinkStore) poolPath(id string) string { return filepath.Join(s.pool, id[:2], id) }
func (s *HardlinkStore) refPath(id string) string  { return filepath.Join(s.refs, id[:2], id) }

// Put writes data to the pool if absent, then hard-links it into this namespace.
// Both steps are idempotent.
func (s *HardlinkStore) Put(data []byte) (string, error) {
	id := ID(data)
	pp := s.poolPath(id)
	if _, err := os.Stat(pp); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(pp), 0o700); err != nil {
			return "", err
		}
		if err := writeAtomic(pp, data); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	rp := s.refPath(id)
	if err := os.MkdirAll(filepath.Dir(rp), 0o700); err != nil {
		return "", err
	}
	if err := os.Link(pp, rp); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("link blob: %w", err)
	}
	return id, nil
}

// Get returns the bytes for id from this namespace, or ErrNotFound.
func (s *HardlinkStore) Get(id string) ([]byte, error) {
	data, err := os.ReadFile(s.refPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Has reports whether this namespace references id.
func (s *HardlinkStore) Has(id string) (bool, error) {
	_, err := os.Stat(s.refPath(id))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Delete unlinks this namespace's reference and, if no repository references the
// blob anymore (only the pool's own link remains), removes the pooled copy too.
func (s *HardlinkStore) Delete(id string) error {
	rp := s.refPath(id)
	if err := fsx.Remove(rp); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	pp := s.poolPath(id)
	n, err := linkCount(pp)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if n <= 1 { // only the pool link remains; nothing references it
		if err := fsx.Remove(pp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// List returns the content addresses this namespace references.
func (s *HardlinkStore) List() ([]string, error) {
	var ids []string
	err := filepath.WalkDir(s.refs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			ids = append(ids, d.Name())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// writeAtomic writes data to a temp file in the same directory and renames it
// into place, so a pool entry never appears partially written.
func writeAtomic(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+randHex())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := fsx.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func randHex() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
