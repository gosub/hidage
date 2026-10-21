package repo

import (
	"os"
	"syscall"
)

// onlineOnly reports an iCloud Drive file evicted to make room ("dataless"):
// reading it would download it in full, so it is left out instead.
func onlineOnly(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	const sfDataless = 0x40000000 // SF_DATALESS
	return ok && st.Flags&sfDataless != 0
}

// inUse reports an error from a file another program holds locked. macOS lets
// open files be read, so this never happens.
func inUse(error) bool { return false }
