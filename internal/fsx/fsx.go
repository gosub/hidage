// Package fsx wraps the file operations that Windows refuses while another
// process has the file open: renaming over it and removing it. The CLI and the
// daemon share files, and antivirus scanners and the search indexer open files
// briefly, so on Windows these retry for a short while before giving up. On
// other systems they are plain os.Rename and os.Remove.
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
