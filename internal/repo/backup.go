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
	TotalObjects int // objects across both stores after the snapshot
	NewObjects   int // objects this snapshot actually wrote (the delta)
	Pruned       int // objects reclaimed when --replace dropped the previous snapshot
}

// Snapshot captures the repository's tracked folder. With replace set, it
// supersedes the previous latest snapshot (chaining past it) and prunes objects
// only that snapshot held. priv is the owner's signing key.
func (r *Repo) Snapshot(priv ed25519.PrivateKey, replace bool) (*Snapshot, *SnapshotStats, error) {
	info, err := os.Stat(r.Folder)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", r.Folder, err)
	}

	prev, err := r.latest()
	if err != nil {
		return nil, nil, err
	}

	before, err := r.objectIDs()
	if err != nil {
		return nil, nil, err
	}

	b := dag.NewBuilder(r.meta, r.data, r.masterKey)
	root, err := r.buildRoot(b, r.Folder, info)
	if err != nil {
		return nil, nil, err
	}

	// Normally chain to the previous snapshot; when replacing, chain past it so
	// the history stays valid after it is forgotten.
	parentID := ""
	if prev != nil {
		if replace {
			parentID = prev.Parent
		} else {
			parentID = prev.ID
		}
	}

	snap := &Snapshot{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Parent:    parentID,
		Source:    filepath.Base(r.Folder),
		Root:      root,
	}
	if err := snap.Sign(priv); err != nil {
		return nil, nil, err
	}
	if err := r.saveSnapshot(snap); err != nil {
		return nil, nil, err
	}

	after, err := r.objectIDs()
	if err != nil {
		return nil, nil, err
	}
	stats := &SnapshotStats{TotalObjects: len(after), NewObjects: len(after) - len(before)}

	if replace && prev != nil {
		if err := r.Forget(prev.ID); err != nil {
			return nil, nil, err
		}
		ps, err := r.Prune()
		if err != nil {
			return nil, nil, err
		}
		stats.Pruned = ps.Deleted
		stats.TotalObjects -= ps.Deleted
	}
	return snap, stats, nil
}

// buildRoot wraps the tracked folder under a single top-level entry named after
// it, so it restores under its original name.
func (r *Repo) buildRoot(b *dag.Builder, src string, info os.FileInfo) (dag.Ref, error) {
	ref, err := r.buildTree(b, src)
	if err != nil {
		return dag.Ref{}, err
	}
	entry := dag.Entry{Name: filepath.Base(src), Kind: dag.KindTree, Mode: uint32(info.Mode().Perm()), Ref: ref}
	return b.PutTree(dag.TreeNode{Entries: []dag.Entry{entry}})
}

// buildTree recurses a directory, skipping the .hidage metadata directory.
// os.ReadDir returns entries sorted by name, so builds are deterministic.
func (r *Repo) buildTree(b *dag.Builder, dir string) (dag.Ref, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dag.Ref{}, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var tn dag.TreeNode
	for _, de := range entries {
		if de.Name() == hidageDir {
			continue // never back up our own metadata
		}
		full := filepath.Join(dir, de.Name())
		fi, err := de.Info()
		if err != nil {
			return dag.Ref{}, err
		}
		switch {
		case de.IsDir():
			ref, err := r.buildTree(b, full)
			if err != nil {
				return dag.Ref{}, err
			}
			tn.Entries = append(tn.Entries, dag.Entry{Name: de.Name(), Kind: dag.KindTree, Mode: uint32(fi.Mode().Perm()), Ref: ref})
		case fi.Mode().IsRegular():
			ref, err := r.buildFile(b, full, fi)
			if err != nil {
				return dag.Ref{}, err
			}
			tn.Entries = append(tn.Entries, dag.Entry{Name: de.Name(), Kind: dag.KindFile, Mode: uint32(fi.Mode().Perm()), Ref: ref})
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

// objectIDs returns the union of object IDs across the meta and data stores.
func (r *Repo) objectIDs() (map[string]struct{}, error) {
	set := make(map[string]struct{})
	for _, st := range []interface{ List() ([]string, error) }{r.meta, r.data} {
		ids, err := st.List()
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			set[id] = struct{}{}
		}
	}
	return set, nil
}
