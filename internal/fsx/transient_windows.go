package fsx

import (
	"errors"

	"golang.org/x/sys/windows"
)

// transient reports whether err is a sharing or lock violation (another
// process has the file open), or the access denial Windows also returns for a
// file that is being deleted.
func transient(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
