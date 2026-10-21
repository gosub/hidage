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

// MakePrivate replaces dir's access list, and that of everything under it, with
// one granting full control to the current user and SYSTEM only, so a folder
// on a drive readable by all users (such as D:\) is not readable by them.
func MakePrivate(dir string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
