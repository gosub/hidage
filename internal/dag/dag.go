// Package dag implements the Merkle DAG that represents a snapshot's file tree.
// Every object — a file's data chunk, a file node, a directory (tree) node — is
// serialized, deterministically encrypted, and stored content-addressed. A
// parent refers to a child by a Ref (fingerprint + store address), so an
// unchanged subtree keeps the same Ref across snapshots and is never re-stored.
package dag

import (
	"encoding/json"
	"fmt"
	"sort"

	"hidage/internal/crypto"
	"hidage/internal/store"
)

// Ref points at one stored, encrypted object. Fp is the fingerprint needed
// (with the repo master key) to decrypt it; CipherID is its address in the
// store; Size is the plaintext size (informational).
type Ref struct {
	Fp       []byte `json:"fp"`
	CipherID string `json:"cipherID"`
	Size     int64  `json:"size"`
}

// Equal reports whether two refs point at the same object.
func (r Ref) Equal(o Ref) bool {
	return r.CipherID == o.CipherID
}

// Kind values for tree entries.
const (
	KindFile = "file"
	KindTree = "tree"
)

// FileNode lists the ordered chunks that compose a file.
type FileNode struct {
	Type   string `json:"type"` // always "file"
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	Chunks []Ref  `json:"chunks"`
}

// Entry is one child of a directory.
type Entry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // KindFile | KindTree
	Mode uint32 `json:"mode"`
	Ref  Ref    `json:"ref"`
}

// TreeNode is a directory: a name-sorted list of entries.
type TreeNode struct {
	Type    string  `json:"type"` // always "tree"
	Entries []Entry `json:"entries"`
}

// Builder writes objects under a master key, routing by kind: bulk file data
// (chunks) goes to the data store, while the small structure nodes (file/tree)
// go to the meta store. This is what lets the metadata live in a per-folder
// .hidage/ while the chunk bulk lives in a node-wide store outside $HOME.
type Builder struct {
	meta      store.Store
	data      store.Store
	masterKey []byte
}

// NewBuilder returns a Builder writing structure nodes to meta and chunks to
// data, keyed by masterKey.
func NewBuilder(meta, data store.Store, masterKey []byte) *Builder {
	return &Builder{meta: meta, data: data, masterKey: masterKey}
}

// put seals plaintext and stores it in st, returning a Ref. Deterministic
// encryption plus content addressing means re-putting identical bytes is a
// no-op, which is the whole basis of cross-snapshot dedup.
func (b *Builder) put(st store.Store, plaintext []byte, size int64) (Ref, error) {
	ct, fp, err := crypto.SealDeterministic(b.masterKey, plaintext)
	if err != nil {
		return Ref{}, err
	}
	id, err := st.Put(ct)
	if err != nil {
		return Ref{}, err
	}
	return Ref{Fp: fp, CipherID: id, Size: size}, nil
}

// PutChunk stores a raw file-data chunk in the data store.
func (b *Builder) PutChunk(data []byte) (Ref, error) {
	return b.put(b.data, data, int64(len(data)))
}

// PutFile stores a file node in the meta store.
func (b *Builder) PutFile(n FileNode) (Ref, error) {
	n.Type = KindFile
	data, err := json.Marshal(n)
	if err != nil {
		return Ref{}, err
	}
	return b.put(b.meta, data, n.Size)
}

// PutTree stores a tree node (entries name-sorted for determinism) in meta.
func (b *Builder) PutTree(n TreeNode) (Ref, error) {
	n.Type = KindTree
	sort.Slice(n.Entries, func(i, j int) bool { return n.Entries[i].Name < n.Entries[j].Name })
	data, err := json.Marshal(n)
	if err != nil {
		return Ref{}, err
	}
	return b.put(b.meta, data, 0)
}

// Loader reads objects back out of the meta and data stores under a master key.
type Loader struct {
	meta      store.Store
	data      store.Store
	masterKey []byte
}

// NewLoader returns a Loader reading structure nodes from meta and chunks from
// data, keyed by masterKey.
func NewLoader(meta, data store.Store, masterKey []byte) *Loader {
	return &Loader{meta: meta, data: data, masterKey: masterKey}
}

func (l *Loader) open(st store.Store, ref Ref) ([]byte, error) {
	ct, err := st.Get(ref.CipherID)
	if err != nil {
		return nil, err
	}
	return crypto.OpenDeterministic(l.masterKey, ref.Fp, ct)
}

// Chunk returns raw chunk data from the data store.
func (l *Loader) Chunk(ref Ref) ([]byte, error) { return l.open(l.data, ref) }

// File loads a file node from the meta store.
func (l *Loader) File(ref Ref) (FileNode, error) {
	data, err := l.open(l.meta, ref)
	if err != nil {
		return FileNode{}, err
	}
	var n FileNode
	if err := json.Unmarshal(data, &n); err != nil {
		return FileNode{}, err
	}
	if n.Type != KindFile {
		return FileNode{}, fmt.Errorf("expected file node, got %q", n.Type)
	}
	return n, nil
}

// Tree loads a tree node from the meta store.
func (l *Loader) Tree(ref Ref) (TreeNode, error) {
	data, err := l.open(l.meta, ref)
	if err != nil {
		return TreeNode{}, err
	}
	var n TreeNode
	if err := json.Unmarshal(data, &n); err != nil {
		return TreeNode{}, err
	}
	if n.Type != KindTree {
		return TreeNode{}, fmt.Errorf("expected tree node, got %q", n.Type)
	}
	return n, nil
}
