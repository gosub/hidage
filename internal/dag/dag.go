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

// Builder writes objects into a store under a master key.
type Builder struct {
	store     store.Store
	masterKey []byte
}

// NewBuilder returns a Builder backed by st and keyed by masterKey.
func NewBuilder(st store.Store, masterKey []byte) *Builder {
	return &Builder{store: st, masterKey: masterKey}
}

// put seals plaintext and stores it, returning a Ref. Deterministic encryption
// plus the store's content addressing means re-putting identical bytes is a
// no-op, which is the whole basis of cross-snapshot dedup.
func (b *Builder) put(plaintext []byte, size int64) (Ref, error) {
	ct, fp, err := crypto.SealDeterministic(b.masterKey, plaintext)
	if err != nil {
		return Ref{}, err
	}
	id, err := b.store.Put(ct)
	if err != nil {
		return Ref{}, err
	}
	return Ref{Fp: fp, CipherID: id, Size: size}, nil
}

// PutChunk stores a raw file-data chunk.
func (b *Builder) PutChunk(data []byte) (Ref, error) {
	return b.put(data, int64(len(data)))
}

// PutFile stores a file node.
func (b *Builder) PutFile(n FileNode) (Ref, error) {
	n.Type = KindFile
	data, err := json.Marshal(n)
	if err != nil {
		return Ref{}, err
	}
	return b.put(data, n.Size)
}

// PutTree stores a tree node, sorting entries by name for determinism.
func (b *Builder) PutTree(n TreeNode) (Ref, error) {
	n.Type = KindTree
	sort.Slice(n.Entries, func(i, j int) bool { return n.Entries[i].Name < n.Entries[j].Name })
	data, err := json.Marshal(n)
	if err != nil {
		return Ref{}, err
	}
	return b.put(data, 0)
}

// Loader reads objects back out of a store under a master key.
type Loader struct {
	store     store.Store
	masterKey []byte
}

// NewLoader returns a Loader for st keyed by masterKey.
func NewLoader(st store.Store, masterKey []byte) *Loader {
	return &Loader{store: st, masterKey: masterKey}
}

func (l *Loader) open(ref Ref) ([]byte, error) {
	ct, err := l.store.Get(ref.CipherID)
	if err != nil {
		return nil, err
	}
	return crypto.OpenDeterministic(l.masterKey, ref.Fp, ct)
}

// Chunk returns raw chunk data.
func (l *Loader) Chunk(ref Ref) ([]byte, error) { return l.open(ref) }

// File loads a file node.
func (l *Loader) File(ref Ref) (FileNode, error) {
	data, err := l.open(ref)
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

// Tree loads a tree node.
func (l *Loader) Tree(ref Ref) (TreeNode, error) {
	data, err := l.open(ref)
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
