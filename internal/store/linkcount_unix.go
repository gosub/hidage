//go:build !windows

package store

import (
	"fmt"
	"os"
	"syscall"
)

// linkCount returns the number of hard links to the file at path.
func linkCount(path string) (uint64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("hardlink store: no link count for %s", path)
	}
	return uint64(st.Nlink), nil
}
