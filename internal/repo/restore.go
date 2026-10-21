package repo

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"hidage/internal/dag"
)

// Restore materializes a snapshot's tree under outDir from this repository's
// local stores.
func (r *Repo) Restore(snap *Snapshot, outDir string) error {
	return RestoreWith(dag.NewLoader(r.meta, r.data, r.masterKey), snap, outDir)
}

// RestoreWith materializes a snapshot's tree under outDir using any loader —
// local stores or a network-backed one (for recovery from peers). Files are
// streamed chunk by chunk, so memory stays bounded; path-traversal entry names
// are rejected.
func RestoreWith(l *dag.Loader, snap *Snapshot, outDir string) error {
	return (&Restorer{Loader: l}).All(snap, outDir)
}

// Restorer restores snapshots and records the entries it had to rename (see
// Renamed), so the caller can report them.
type Restorer struct {
	Loader  *dag.Loader
	Renamed []Renamed
}

// All restores the whole snapshot under outDir, like RestoreWith.
func (r *Restorer) All(snap *Snapshot, outDir string) error {
	if err := snap.Verify(); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	root, err := r.Loader.Tree(snap.Root)
	if err != nil {
		return err
	}
	names := dirNames{}
	for _, e := range root.Entries {
		if err := r.materialize(e, outDir, "", names); err != nil {
			return err
		}
	}
	return nil
}

// materialize restores entry e into parentDir. from is the snapshot path of
// parentDir and names the names already used in it.
func (r *Restorer) materialize(e dag.Entry, parentDir, from string, names dirNames) error {
	name := names.claim(e.Name)
	target, err := safeJoin(parentDir, name)
	if err != nil {
		return err
	}
	from = path.Join(from, e.Name)
	if name != e.Name {
		r.Renamed = append(r.Renamed, Renamed{From: from, To: target})
	}
	switch e.Kind {
	case dag.KindTree:
		if err := os.MkdirAll(target, fs.FileMode(e.Mode)|0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", target, err)
		}
		tn, err := r.Loader.Tree(e.Ref)
		if err != nil {
			return err
		}
		children := dirNames{}
		for _, child := range tn.Entries {
			if err := r.materialize(child, target, from, children); err != nil {
				return err
			}
		}
		// Set the directory mtime last: writing children would otherwise bump it.
		restoreModTime(target, e.ModTime)
	case dag.KindFile:
		fn, err := r.Loader.File(e.Ref)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(e.Mode)|0o600)
		if err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}
		for _, cref := range fn.Chunks {
			data, err := r.Loader.Chunk(cref)
			if err != nil {
				out.Close()
				return err
			}
			if _, err := out.Write(data); err != nil {
				out.Close()
				return err
			}
		}
		if err := out.Close(); err != nil {
			return err
		}
		restoreModTime(target, e.ModTime)
	default:
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	return nil
}

// restoreModTime applies a recorded modification time (unix nanoseconds) to a
// restored path. Zero means the snapshot predates mtime recording, so it is left
// at the current time; failures are non-fatal (the data is already written).
func restoreModTime(path string, modTime int64) {
	if modTime == 0 {
		return
	}
	t := time.Unix(0, modTime)
	_ = os.Chtimes(path, t, t)
}

// safeJoin joins a child name onto a directory, rejecting traversal.
func safeJoin(dir, name string) (string, error) {
	target := filepath.Join(dir, name)
	cleanDir := filepath.Clean(dir) + string(os.PathSeparator)
	if target != filepath.Clean(dir) && !strings.HasPrefix(target, cleanDir) {
		return "", fmt.Errorf("unsafe entry name %q", name)
	}
	return target, nil
}

// RestorePath restores one file or directory of a snapshot into outDir (under
// its own base name). path is relative to the snapshot's folder, with "/"
// separators; "" or "." restores the whole snapshot like RestoreWith.
func RestorePath(l *dag.Loader, snap *Snapshot, path, outDir string) error {
	return (&Restorer{Loader: l}).Path(snap, path, outDir)
}

// Path restores one file or directory of a snapshot into outDir, like
// RestorePath.
func (r *Restorer) Path(snap *Snapshot, p, outDir string) error {
	if cleanPath(p) == "" {
		return r.All(snap, outDir)
	}
	if err := snap.Verify(); err != nil {
		return err
	}
	e, err := findEntry(r.Loader, snap, p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	return r.materialize(e, outDir, path.Dir(cleanPath(p)), dirNames{})
}

// List returns the entries of a directory in a snapshot (path relative to the
// snapshot's folder; "" for its top level), or the entry itself for a file.
func List(l *dag.Loader, snap *Snapshot, path string) ([]dag.Entry, error) {
	e, err := findEntry(l, snap, path)
	if err != nil {
		return nil, err
	}
	if e.Kind != dag.KindTree {
		return []dag.Entry{e}, nil
	}
	t, err := l.Tree(e.Ref)
	if err != nil {
		return nil, err
	}
	return t.Entries, nil
}

// findEntry walks path (relative to the snapshot's folder) down from the root.
func findEntry(l *dag.Loader, snap *Snapshot, path string) (dag.Entry, error) {
	root, err := l.Tree(snap.Root)
	if err != nil {
		return dag.Entry{}, err
	}
	if len(root.Entries) != 1 {
		return dag.Entry{}, fmt.Errorf("snapshot root has %d entries, want 1", len(root.Entries))
	}
	e := root.Entries[0]
	path = cleanPath(path)
	if path == "" {
		return e, nil
	}
	for _, part := range strings.Split(path, "/") {
		if e.Kind != dag.KindTree {
			return dag.Entry{}, fmt.Errorf("%s: not a directory in this snapshot", path)
		}
		t, err := l.Tree(e.Ref)
		if err != nil {
			return dag.Entry{}, err
		}
		found := false
		for _, c := range t.Entries {
			if c.Name == part {
				e, found = c, true
				break
			}
		}
		if !found {
			return dag.Entry{}, fmt.Errorf("%s: no such file or directory in this snapshot", path)
		}
	}
	return e, nil
}

func cleanPath(p string) string {
	p = strings.Trim(filepath.ToSlash(filepath.Clean("/"+p)), "/")
	if p == "." {
		return ""
	}
	return p
}

// Loader returns a loader over this repository's local stores.
func (r *Repo) Loader() *dag.Loader { return dag.NewLoader(r.meta, r.data, r.masterKey) }
