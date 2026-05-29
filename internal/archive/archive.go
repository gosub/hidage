// Package archive normalizes the thing being backed up into a single byte
// stream. A regular file is passed through unchanged; a directory is packed
// into a deterministic tar stream (entries sorted, timestamps zeroed) so the
// same tree always yields the same bytes. Restore reverses the process.
package archive

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Pack reads path and returns its contents as a single blob. isArchive reports
// whether the blob is a tar stream (directory) versus raw file bytes; name is
// the base name of the input, used to recreate it on restore.
func Pack(path string) (blob []byte, isArchive bool, name string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, "", fmt.Errorf("stat %s: %w", path, err)
	}
	name = filepath.Base(strings.TrimRight(path, string(os.PathSeparator)))

	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, false, "", fmt.Errorf("read %s: %w", path, err)
		}
		return data, false, name, nil
	}

	blob, err = packDir(path)
	if err != nil {
		return nil, false, "", err
	}
	return blob, true, name, nil
}

// packDir walks root and writes a deterministic tar archive. Entry names are
// relative to root so the tree can be recreated under any destination.
func packDir(root string) ([]byte, error) {
	var entries []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(entries)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range entries {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("lstat %s: %w", abs, err)
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			// Skip symlinks, devices, sockets in this MVP.
			continue
		}
		hdr := &tar.Header{
			Name: rel,
			Mode: int64(info.Mode().Perm()),
		}
		if info.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Name = rel + "/"
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write header %s: %w", rel, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			f, err := os.Open(abs)
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", abs, err)
			}
			if _, err := io.Copy(tw, f); err != nil {
				f.Close()
				return nil, fmt.Errorf("copy %s: %w", abs, err)
			}
			f.Close()
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return buf.Bytes(), nil
}

// Unpack writes a blob produced by Pack into destDir. For a raw file the blob
// is written to destDir/name. For an archive the tree is recreated under
// destDir/name. It guards against path-traversal entries.
func Unpack(blob []byte, isArchive bool, destDir, name string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}
	if !isArchive {
		target := filepath.Join(destDir, name)
		if err := os.WriteFile(target, blob, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	}

	root := filepath.Join(destDir, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create archive root: %w", err)
	}

	tr := tar.NewReader(bytes.NewReader(blob))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		target, err := safeJoin(root, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs.FileMode(hdr.Mode)|0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(hdr.Mode)|0o600)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			f.Close()
		}
	}
	return nil
}

// safeJoin joins root and a tar entry name, rejecting paths that escape root.
func safeJoin(root, name string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(name))
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	if target != filepath.Clean(root) && !strings.HasPrefix(target, cleanRoot) {
		return "", fmt.Errorf("unsafe path in archive: %q", name)
	}
	return target, nil
}
