//go:build !windows

package main

import "syscall"

// processAlive reports whether a process with this pid exists.
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
