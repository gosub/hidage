//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// processAlive reports whether a process with this pid exists.
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// shellCommand runs a command line from config.toml through the shell.
func shellCommand(line string) *exec.Cmd { return exec.Command("sh", "-c", line) }
