//go:build !windows

package fsx

// transient reports whether err may clear up on retry. Unix lets a file be
// renamed over or removed while it is open, so nothing is retried.
func transient(error) bool { return false }
