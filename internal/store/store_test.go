package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestPutGetHasDelete(t *testing.T) {
	s, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("shard contents")
	id, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if id != ID(data) {
		t.Fatalf("Put returned %s, want %s", id, ID(data))
	}

	ok, err := s.Has(id)
	if err != nil || !ok {
		t.Fatalf("Has = %v, %v", ok, err)
	}

	got, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Get returned wrong data")
	}

	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("dup")
	id1, _ := s.Put(data)
	id2, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("idempotent Put returned different ids")
	}
}

func TestListReturnsStoredIDs(t *testing.T) {
	s, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, d := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
		id, _ := s.Put(d)
		want[id] = true
	}
	ids, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(want) {
		t.Fatalf("List returned %d ids, want %d", len(ids), len(want))
	}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected id %s", id)
		}
	}
}
