package store

import (
	"os"

	"golang.org/x/sys/windows"
)

// linkCount returns the number of hard links to the file at path.
func linkCount(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, &os.PathError{Op: "GetFileInformationByHandle", Path: path, Err: err}
	}
	return uint64(info.NumberOfLinks), nil
}
