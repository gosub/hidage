//go:build !windows

package fsx

// transient reports whether err may clear up on retry. Unix lets a file be
// renamed over or removed while it is open, so nothing is retried.
func transient(error) bool { return false }

// MakePrivate restricts dir to the current user. On Unix the 0700 mode the
// directory is created with already does that, so this does nothing.
func MakePrivate(string) error { return nil }
