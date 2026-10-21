package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// processAlive reports whether a process with this pid exists and is still
// running (a handle can outlive the process, so the exit code is checked).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}

// shellCommand runs a command line from config.toml through cmd.exe. The line
// is passed verbatim, since cmd.exe does not parse arguments the way Go quotes
// them.
func shellCommand(line string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + line + `"`}
	return cmd
}
