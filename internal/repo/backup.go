package repo

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"hidage/internal/chunk"
	"hidage/internal/dag"
	"hidage/internal/fsx"
)

// SnapshotStats summarizes what a snapshot stored.
type SnapshotStats struct {
	TotalObjects int      // objects across both stores after the snapshot
	NewObjects   int      // objects this snapshot actually wrote (the delta)
	Pruned       int      // objects reclaimed when --replace dropped the previous snapshot
	Skipped      []string // paths of unsupported entries (symlinks, devices, …) not backed up
	Unreadable   []Skip   // files and folders that could not be read, left out of the snapshot
	Unchanged    bool     // SnapshotIfChanged found nothing new and kept the latest snapshot
}

// Skip is a file or folder left out of a snapshot because it could not be
// read: locked or in use, no permission, gone mid-backup, or online-only.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Snapshot captures the repository's tracked folder. With replace set, it
// supersedes the previous latest snapshot (chaining past it) and prunes objects
// only that snapshot held. priv is the owner's signing key.
func (r *Repo) Snapshot(priv ed25519.PrivateKey, replace bool) (*Snapshot, *SnapshotStats, error) {
	return r.snapshot(priv, replace, false)
}

// SnapshotIfChanged takes a snapshot only if the folder differs from the latest
// one; otherwise it returns the latest snapshot with stats.Unchanged set. It is
// what scheduled backups use, so an idle folder doesn't pile up identical
// snapshots.
func (r *Repo) SnapshotIfChanged(priv ed25519.PrivateKey) (*Snapshot, *SnapshotStats, error) {
	return r.snapshot(priv, false, true)
}

func (r *Repo) snapshot(priv ed25519.PrivateKey, replace, skipUnchanged bool) (*Snapshot, *SnapshotStats, error) {
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
	w := &walk{}
	root, err := r.buildRoot(b, r.Folder, info, w)
	if err != nil {
		return nil, nil, err
	}
	if err := r.saveUnreadable(w.unreadable); err != nil {
		return nil, nil, err
	}
	if skipUnchanged && prev != nil && prev.Root.Equal(root) {
		return prev, &SnapshotStats{TotalObjects: len(before), Skipped: w.skipped, Unreadable: w.unreadable, Unchanged: true}, nil
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
	stats := &SnapshotStats{TotalObjects: len(after), NewObjects: len(after) - len(before), Skipped: w.skipped, Unreadable: w.unreadable}

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

// walk collects what a snapshot leaves out.
type walk struct {
	skipped    []string // unsupported entries: symlinks, devices, sockets, ...
	unreadable []Skip
}

func (w *walk) unread(path string, err error) {
	w.unreadable = append(w.unreadable, Skip{Path: path, Reason: reason(err)})
}

// reason turns a read error into a short explanation for the user.
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "removed during the backup"
	case inUse(err):
		return "in use by another program"
	}
	return err.Error()
}

// buildRoot wraps the tracked folder under a single top-level entry named after
// it, so it restores under its original name.
func (r *Repo) buildRoot(b *dag.Builder, src string, info os.FileInfo, w *walk) (dag.Ref, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return dag.Ref{}, fmt.Errorf("read dir %s: %w", src, err)
	}
	ref, err := r.buildTree(b, src, entries, w)
	if err != nil {
		return dag.Ref{}, err
	}
	entry := dag.Entry{Name: filepath.Base(src), Kind: dag.KindTree, Mode: uint32(info.Mode().Perm()), ModTime: info.ModTime().UnixNano(), Ref: ref}
	return b.PutTree(dag.TreeNode{Entries: []dag.Entry{entry}})
}

// buildTree builds the tree of a directory from its entries, skipping the
// .hidage metadata directory. Unsupported entries (via w.skipped) and files or
// subfolders that can't be read (via w.unreadable) are left out and recorded,
// so one locked file doesn't fail the whole backup. os.ReadDir returns entries
// sorted by name, so builds are deterministic.
func (r *Repo) buildTree(b *dag.Builder, dir string, entries []os.DirEntry, w *walk) (dag.Ref, error) {
	var tn dag.TreeNode
	for _, de := range entries {
		if de.Name() == hidageDir {
			continue // never back up our own metadata
		}
		full := filepath.Join(dir, de.Name())
		fi, err := de.Info()
		if err != nil {
			w.unread(full, err)
			continue
		}
		switch {
		case onlineOnly(fi):
			w.unreadable = append(w.unreadable, Skip{Path: full, Reason: "online-only, not downloaded"})
		case de.IsDir():
			sub, err := os.ReadDir(full)
			if err != nil {
				w.unread(full, err)
				continue
			}
			ref, err := r.buildTree(b, full, sub, w)
			if err != nil {
				return dag.Ref{}, err
			}
			tn.Entries = append(tn.Entries, dag.Entry{Name: de.Name(), Kind: dag.KindTree, Mode: uint32(fi.Mode().Perm()), ModTime: fi.ModTime().UnixNano(), Ref: ref})
		case fi.Mode().IsRegular():
			ref, readErr, err := r.buildFile(b, full, fi)
			if err != nil {
				return dag.Ref{}, err
			}
			if readErr != nil {
				w.unread(full, readErr)
				continue
			}
			tn.Entries = append(tn.Entries, dag.Entry{Name: de.Name(), Kind: dag.KindFile, Mode: uint32(fi.Mode().Perm()), ModTime: fi.ModTime().UnixNano(), Ref: ref})
		default:
			// Symlinks, devices, sockets, etc. are not represented in the DAG;
			// record them so the snapshot can report what it did not capture
			// rather than dropping them silently.
			w.skipped = append(w.skipped, full)
		}
	}
	return b.PutTree(tn)
}

// buildFile content-defined-chunks a file and stores each chunk, retaining only
// the small chunk refs. Memory stays bounded regardless of file size. A failure
// to open or read the file is returned as readErr, so the caller can skip it;
// err is a failure to store, which ends the snapshot. Chunks stored before a
// read error are reclaimed by the next prune.
func (r *Repo) buildFile(b *dag.Builder, path string, info os.FileInfo) (ref dag.Ref, readErr, err error) {
	f, err := os.Open(path)
	if err != nil {
		return dag.Ref{}, err, nil
	}
	defer f.Close()

	src := &errReader{r: f}
	var chunks []dag.Ref
	var size int64
	err = chunk.Split(src, func(data []byte) error {
		ref, err := b.PutChunk(data)
		if err != nil {
			return err
		}
		chunks = append(chunks, ref)
		size += int64(len(data))
		return nil
	})
	if src.err != nil {
		return dag.Ref{}, src.err, nil
	}
	if err != nil {
		return dag.Ref{}, nil, fmt.Errorf("chunk %s: %w", path, err)
	}
	ref, err = b.PutFile(dag.FileNode{Mode: uint32(info.Mode().Perm()), Size: size, ModTime: info.ModTime().UnixNano(), Chunks: chunks})
	return ref, nil, err
}

// errReader remembers the first read error other than EOF, telling a file that
// can't be read apart from a store that can't be written.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}

// unreadablePath is where the last snapshot's unreadable list is kept, for
// `hidage status`.
func (r *Repo) unreadablePath() string { return r.HidageFile("unreadable.json") }

func (r *Repo) saveUnreadable(list []Skip) error {
	if len(list) == 0 {
		if err := fsx.Remove(r.unreadablePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.unreadablePath(), data, 0o600)
}

// LastUnreadable returns what the latest snapshot had to leave out because it
// could not be read.
func (r *Repo) LastUnreadable() []Skip {
	data, err := os.ReadFile(r.unreadablePath())
	if err != nil {
		return nil
	}
	var list []Skip
	json.Unmarshal(data, &list)
	return list
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
