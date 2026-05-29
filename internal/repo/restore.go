package repo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hidage/internal/dag"
)

// Restore materializes a snapshot's tree under outDir, recreating the original
// top-level name. Files are streamed chunk by chunk, so memory stays bounded.
func (r *Repo) Restore(snap *Snapshot, outDir string) error {
	if err := snap.Verify(); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	l := dag.NewLoader(r.meta, r.data, r.masterKey)
	root, err := l.Tree(snap.Root)
	if err != nil {
		return err
	}
	for _, e := range root.Entries {
		if err := r.materialize(l, e, outDir); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) materialize(l *dag.Loader, e dag.Entry, parentDir string) error {
	target, err := safeJoin(parentDir, e.Name)
	if err != nil {
		return err
	}
	switch e.Kind {
	case dag.KindTree:
		if err := os.MkdirAll(target, fs.FileMode(e.Mode)|0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", target, err)
		}
		tn, err := l.Tree(e.Ref)
		if err != nil {
			return err
		}
		for _, child := range tn.Entries {
			if err := r.materialize(l, child, target); err != nil {
				return err
			}
		}
	case dag.KindFile:
		fn, err := l.File(e.Ref)
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
			data, err := l.Chunk(cref)
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
	default:
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	return nil
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
