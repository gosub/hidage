//go:build !windows && !darwin

package repo

import "os"

// onlineOnly reports a file whose content lives only online. Linux has no
// such placeholders in the filesystem.
func onlineOnly(os.FileInfo) bool { return false }

// inUse reports an error from a file another program holds locked. Linux
// lets open files be read, so this never happens.
func inUse(error) bool { return false }
