package store

import (
	"os"
	"testing"
	"time"
)

func TestReapExpired(t *testing.T) {
	s, err := NewKeyedStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Put("aa11", []byte("old"))
	s.Put("bb22", []byte("fresh"))
	// Age "aa11" past the lease by backdating its mtime.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(s.path("aa11"), old, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.Reap(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if ok, _ := s.Has("aa11"); ok {
		t.Fatal("expired object should be gone")
	}
	if ok, _ := s.Has("bb22"); !ok {
		t.Fatal("fresh object should remain")
	}
}

func TestReapRenewedByRewrite(t *testing.T) {
	s, _ := NewKeyedStore(t.TempDir())
	s.Put("cc33", []byte("v1"))
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(s.path("cc33"), old, old)
	// Re-Put (a renewal/republish) refreshes the mtime, so it survives a reap.
	s.Put("cc33", []byte("v1"))
	n, _ := s.Reap(24 * time.Hour)
	if n != 0 {
		t.Fatalf("renewed object should not be reaped, reaped %d", n)
	}
}
