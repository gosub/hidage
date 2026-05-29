package dag

import (
	"bytes"
	"testing"

	"hidage/internal/crypto"
	"hidage/internal/store"
)

// newTestStores returns separate meta and data stores plus a master key.
func newTestStores(t *testing.T) (meta, data store.Store, mk []byte) {
	t.Helper()
	m, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk, _ = crypto.NewDEK()
	return m, d, mk
}

func TestChunkAndFileRoundtrip(t *testing.T) {
	meta, data, mk := newTestStores(t)
	b := NewBuilder(meta, data, mk)
	l := NewLoader(meta, data, mk)

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

func TestChunksGoToDataStore(t *testing.T) {
	meta, data, mk := newTestStores(t)
	b := NewBuilder(meta, data, mk)

	r1, _ := b.PutChunk([]byte("duplicate"))
	r2, _ := b.PutChunk([]byte("duplicate"))
	if !r1.Equal(r2) {
		t.Fatal("identical chunks should share a ref")
	}
	dataIDs, _ := data.List()
	if len(dataIDs) != 1 {
		t.Fatalf("expected 1 chunk in data store, got %d", len(dataIDs))
	}
	// A file node lands in meta, not data.
	if _, err := b.PutFile(FileNode{Size: 9, Chunks: []Ref{r1}}); err != nil {
		t.Fatal(err)
	}
	metaIDs, _ := meta.List()
	if len(metaIDs) != 1 {
		t.Fatalf("expected 1 node in meta store, got %d", len(metaIDs))
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
	meta, data, mk := newTestStores(t)
	b := NewBuilder(meta, data, mk)
	l := NewLoader(meta, data, mk)

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
	meta, data, mk := newTestStores(t)
	b := NewBuilder(meta, data, mk)
	l := NewLoader(meta, data, mk)
	root := buildTree(t, b, map[string]string{"a": "1", "b": "2"})

	reach, err := l.Reachable(root)
	if err != nil {
		t.Fatal(err)
	}
	metaIDs, _ := meta.List()
	dataIDs, _ := data.List()
	if len(reach) != len(metaIDs)+len(dataIDs) {
		t.Fatalf("reachable %d != stored %d (all objects should be reachable)",
			len(reach), len(metaIDs)+len(dataIDs))
	}
}
