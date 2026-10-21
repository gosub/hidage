// Package fsx holds the file operations that differ on Windows.
//
// Windows refuses to rename over or remove a file another process has open.
// The CLI and the daemon share files, and antivirus scanners and the search
// indexer open files briefly, so on Windows Rename and Remove retry for a
// short while before giving up. On other systems they are plain os.Rename and
// os.Remove.
//
// Unix file modes mean nothing on Windows, so MakePrivate gives a directory an
// access list that only the current user can read.
package fsx

import (
	"os"
	"time"
)

// retryFor bounds how long a sharing violation is retried.
const retryFor = 2 * time.Second

// Rename renames oldpath to newpath, replacing newpath if it exists.
func Rename(oldpath, newpath string) error {
	return retry(func() error { return os.Rename(oldpath, newpath) })
}

// Remove removes the named file or empty directory.
func Remove(path string) error {
	return retry(func() error { return os.Remove(path) })
}

func retry(op func() error) error {
	deadline := time.Now().Add(retryFor)
	wait := 10 * time.Millisecond
	for {
		err := op()
		if err == nil || !transient(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, 200*time.Millisecond)
	}
}

// MkdirPrivate creates dir and any missing parents. If dir did not exist yet,
// it is made private to the current user (see MakePrivate), and so is
// everything later created under it.
func MkdirPrivate(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return MakePrivate(dir)
}
