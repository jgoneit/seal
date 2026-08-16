//go:build windows

package runstate

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPrivateStagingDirectory(parent *os.Root, name string) error {
	if err := validateRelativeName(name); err != nil {
		return err
	}

	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	currentUserSID, err := currentUser.User.Sid.Copy()
	if err != nil {
		return err
	}
	localSystemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}

	var pinner runtime.Pinner
	pinner.Pin(currentUserSID)
	pinner.Pin(localSystemSID)
	defer pinner.Unpin()

	allowedSIDs := []*windows.SID{currentUserSID}
	if !currentUserSID.Equals(localSystemSID) {
		allowedSIDs = append(allowedSIDs, localSystemSID)
	}
	entries := make([]windows.EXPLICIT_ACCESS, len(allowedSIDs))
	for index, sid := range allowedSIDs {
		entries[index] = windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	runtime.KeepAlive(entries)
	if err != nil {
		return err
	}
	pinner.Pin(acl)

	parentDirectory, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer parentDirectory.Close()

	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parentDirectory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var directory windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocationSize int64
	err = windows.NtCreateFile(
		&directory,
		windows.FILE_GENERIC_READ|windows.WRITE_DAC|windows.DELETE,
		attributes,
		&status,
		&allocationSize,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|
			windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return ntStatusErrno(err)
	}

	securityErr := windows.SetSecurityInfo(
		directory,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
	closeErr := windows.CloseHandle(directory)
	if securityErr == nil && closeErr == nil {
		return nil
	}
	cleanupErr := parent.RemoveAll(name)
	return errors.Join(securityErr, closeErr, cleanupErr)
}

func ntStatusErrno(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
