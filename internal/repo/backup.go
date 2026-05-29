package repo

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hidage/internal/chunk"
	"hidage/internal/dag"
)

// SnapshotStats summarizes what a snapshot stored.
type SnapshotStats struct {
	TotalObjects int // objects in the store after the snapshot
	NewObjects   int // objects this snapshot actually wrote (the delta)
}

// Snapshot chunks src into the Merkle DAG, stores the objects not already
// present, and writes a signed snapshot root chained to the latest one. priv is
// the owner's signing key.
func (r *Repo) Snapshot(src string, priv ed25519.PrivateKey) (*Snapshot, *SnapshotStats, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", src, err)
	}

	before, err := r.storeIDs()
	if err != nil {
		return nil, nil, err
	}

	b := dag.NewBuilder(r.store, r.masterKey)
	root, err := r.buildRoot(b, src, info)
	if err != nil {
		return nil, nil, err
	}

	parent, err := r.latest()
	if err != nil {
		return nil, nil, err
	}
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}

	snap := &Snapshot{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Parent:    parentID,
		Source:    filepath.Base(src),
		Root:      root,
	}
	if err := snap.Sign(priv); err != nil {
		return nil, nil, err
	}
	if err := r.saveSnapshot(snap); err != nil {
		return nil, nil, err
	}

	after, err := r.storeIDs()
	if err != nil {
		return nil, nil, err
	}
	stats := &SnapshotStats{TotalObjects: len(after), NewObjects: len(after) - len(before)}
	return snap, stats, nil
}

// buildRoot wraps the source under a single top-level entry named after it, so
// both files and directories restore under their original name.
func (r *Repo) buildRoot(b *dag.Builder, src string, info os.FileInfo) (dag.Ref, error) {
	name := filepath.Base(src)
	var entry dag.Entry
	if info.IsDir() {
		ref, err := r.buildTree(b, src)
		if err != nil {
			return dag.Ref{}, err
		}
		entry = dag.Entry{Name: name, Kind: dag.KindTree, Mode: uint32(info.Mode().Perm()), Ref: ref}
	} else {
		ref, err := r.buildFile(b, src, info)
		if err != nil {
			return dag.Ref{}, err
		}
		entry = dag.Entry{Name: name, Kind: dag.KindFile, Mode: uint32(info.Mode().Perm()), Ref: ref}
	}
	return b.PutTree(dag.TreeNode{Entries: []dag.Entry{entry}})
}

// buildTree recurses a directory. os.ReadDir returns entries sorted by name, so
// builds are deterministic.
func (r *Repo) buildTree(b *dag.Builder, dir string) (dag.Ref, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dag.Ref{}, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var tn dag.TreeNode
	for _, de := range entries {
		full := filepath.Join(dir, de.Name())
		info, err := de.Info()
		if err != nil {
			return dag.Ref{}, err
		}
		switch {
		case de.IsDir():
			ref, err := r.buildTree(b, full)
			if err != nil {
				return dag.Ref{}, err
			}
			tn.Entries = append(tn.Entries, dag.Entry{Name: de.Name(), Kind: dag.KindTree, Mode: uint32(info.Mode().Perm()), Ref: ref})
		case info.Mode().IsRegular():
			ref, err := r.buildFile(b, full, info)
			if err != nil {
				return dag.Ref{}, err
			}
			tn.Entries = append(tn.Entries, dag.Entry{Name: de.Name(), Kind: dag.KindFile, Mode: uint32(info.Mode().Perm()), Ref: ref})
		default:
			// Skip symlinks, devices, etc. in this phase.
		}
	}
	return b.PutTree(tn)
}

// buildFile content-defined-chunks a file and stores each chunk, retaining only
// the small chunk refs. Memory stays bounded regardless of file size.
func (r *Repo) buildFile(b *dag.Builder, path string, info os.FileInfo) (dag.Ref, error) {
	f, err := os.Open(path)
	if err != nil {
		return dag.Ref{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var chunks []dag.Ref
	var size int64
	err = chunk.Split(f, func(data []byte) error {
		ref, err := b.PutChunk(data)
		if err != nil {
			return err
		}
		chunks = append(chunks, ref)
		size += int64(len(data))
		return nil
	})
	if err != nil {
		return dag.Ref{}, fmt.Errorf("chunk %s: %w", path, err)
	}
	return b.PutFile(dag.FileNode{Mode: uint32(info.Mode().Perm()), Size: size, Chunks: chunks})
}

// storeIDs returns the set of object IDs currently in the store.
func (r *Repo) storeIDs() (map[string]struct{}, error) {
	ids, err := r.store.List()
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil
}
