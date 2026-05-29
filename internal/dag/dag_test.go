package dag

import (
	"bytes"
	"testing"

	"hidage/internal/crypto"
	"hidage/internal/store"
)

func newTestStore(t *testing.T) (store.Store, []byte) {
	t.Helper()
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk, _ := crypto.NewDEK()
	return st, mk
}

func TestChunkAndFileRoundtrip(t *testing.T) {
	st, mk := newTestStore(t)
	b := NewBuilder(st, mk)
	l := NewLoader(st, mk)

	c1, _ := b.PutChunk([]byte("hello "))
	c2, _ := b.PutChunk([]byte("world"))
	fref, err := b.PutFile(FileNode{Mode: 0o644, Size: 11, Chunks: []Ref{c1, c2}})
	if err != nil {
		t.Fatal(err)
	}

	fn, err := l.File(fref)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, cr := range fn.Chunks {
		d, err := l.Chunk(cr)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(d)
	}
	if buf.String() != "hello world" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestIdenticalChunkDedups(t *testing.T) {
	st, mk := newTestStore(t)
	b := NewBuilder(st, mk)
	r1, _ := b.PutChunk([]byte("duplicate"))
	r2, _ := b.PutChunk([]byte("duplicate"))
	if !r1.Equal(r2) {
		t.Fatal("identical chunks should share a ref")
	}
	ids, _ := st.List()
	if len(ids) != 1 {
		t.Fatalf("expected 1 stored object, got %d", len(ids))
	}
}

func buildTree(t *testing.T, b *Builder, files map[string]string) Ref {
	t.Helper()
	var tn TreeNode
	for name, content := range files {
		c, _ := b.PutChunk([]byte(content))
		fref, _ := b.PutFile(FileNode{Mode: 0o644, Size: int64(len(content)), Chunks: []Ref{c}})
		tn.Entries = append(tn.Entries, Entry{Name: name, Kind: KindFile, Mode: 0o644, Ref: fref})
	}
	ref, err := b.PutTree(tn)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestDiffReportsChanges(t *testing.T) {
	st, mk := newTestStore(t)
	b := NewBuilder(st, mk)
	l := NewLoader(st, mk)

	a := buildTree(t, b, map[string]string{"keep": "same", "edit": "v1", "gone": "x"})
	c := buildTree(t, b, map[string]string{"keep": "same", "edit": "v2", "new": "y"})

	changes, err := l.Diff(a, c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ChangeKind{}
	for _, ch := range changes {
		got[ch.Path] = ch.Kind
	}
	if got["edit"] != Modified {
		t.Errorf("edit: want modified, got %v", got["edit"])
	}
	if got["new"] != Added {
		t.Errorf("new: want added, got %v", got["new"])
	}
	if got["gone"] != Removed {
		t.Errorf("gone: want removed, got %v", got["gone"])
	}
	if _, ok := got["keep"]; ok {
		t.Errorf("keep should be unchanged, got %v", got["keep"])
	}
}

func TestReachableCollectsAll(t *testing.T) {
	st, mk := newTestStore(t)
	b := NewBuilder(st, mk)
	l := NewLoader(st, mk)
	root := buildTree(t, b, map[string]string{"a": "1", "b": "2"})

	reach, err := l.Reachable(root)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := st.List()
	if len(reach) != len(ids) {
		t.Fatalf("reachable %d != stored %d (all objects should be reachable)", len(reach), len(ids))
	}
}
