package repo

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// onlineOnly reports a cloud placeholder (OneDrive "files on demand" and
// similar) whose content lives only online: reading it would download it in
// full, so it is left out instead.
func onlineOnly(fi os.FileInfo) bool {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	const recallOnDataAccess = 0x400000 // FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS
	const recallOnOpen = 0x40000        // FILE_ATTRIBUTE_RECALL_ON_OPEN
	return d.FileAttributes&(recallOnDataAccess|recallOnOpen|windows.FILE_ATTRIBUTE_OFFLINE) != 0
}

// inUse reports an error from a file another program holds open or locked.
func inUse(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
