package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func poolCount(t *testing.T, root string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(filepath.Join(root, "pool"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestHardlinkDedupAcrossNamespaces(t *testing.T) {
	root := t.TempDir()
	a, _ := NewHardlinkStore(root, "repoA")
	b, _ := NewHardlinkStore(root, "repoB")

	data := []byte("shared chunk content")
	idA, err := a.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := b.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if idA != idB {
		t.Fatal("same content must yield same id")
	}
	// Deduplicated on disk: exactly one pooled copy despite two references.
	if n := poolCount(t, root); n != 1 {
		t.Fatalf("expected 1 pooled blob, got %d", n)
	}
	// Both namespaces can read it.
	for _, s := range []*HardlinkStore{a, b} {
		got, err := s.Get(idA)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Get from namespace failed: %v", err)
		}
	}
}

func TestHardlinkRefcountedDelete(t *testing.T) {
	root := t.TempDir()
	a, _ := NewHardlinkStore(root, "repoA")
	b, _ := NewHardlinkStore(root, "repoB")

	data := []byte("two referrers")
	id, _ := a.Put(data)
	b.Put(data)

	// A deletes its reference: B still has it, pool retained.
	if err := a.Delete(id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Has(id); ok {
		t.Fatal("A should no longer reference the blob")
	}
	if ok, _ := b.Has(id); !ok {
		t.Fatal("B must still reference the blob after A deletes it")
	}
	if n := poolCount(t, root); n != 1 {
		t.Fatalf("pool should retain the blob while B references it, got %d", n)
	}
	if _, err := b.Get(id); err != nil {
		t.Fatalf("B should still read the blob: %v", err)
	}

	// B deletes the last reference: pooled copy is reclaimed.
	if err := b.Delete(id); err != nil {
		t.Fatal(err)
	}
	if n := poolCount(t, root); n != 0 {
		t.Fatalf("pool should be empty after last reference removed, got %d", n)
	}
}

func TestHardlinkDeleteMissing(t *testing.T) {
	root := t.TempDir()
	a, _ := NewHardlinkStore(root, "repoA")
	if err := a.Delete(ID([]byte("never stored"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Ensure the pool path is not left with stray temp files after writes.
func TestHardlinkNoTempLeak(t *testing.T) {
	root := t.TempDir()
	a, _ := NewHardlinkStore(root, "repoA")
	a.Put([]byte("x"))
	entries, _ := os.ReadDir(filepath.Join(root, "pool"))
	for _, e := range entries {
		// only the <id[:2]> shard dir should exist at the top level
		if e.Type().IsRegular() {
			t.Fatalf("unexpected file in pool root: %s", e.Name())
		}
	}
}
