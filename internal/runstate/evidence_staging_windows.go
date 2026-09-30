//go:build windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPrivateStagingDirectory(parent *os.Root, name string) (fs.FileInfo, error) {
	if err := validateRelativeName(name); err != nil {
		return nil, err
	}

	var pinner runtime.Pinner
	defer pinner.Unpin()
	securityDescriptor, err := privateSecurityDescriptor(&pinner, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
	if err != nil {
		return nil, err
	}

	parentDirectory, err := parent.Open(".")
	if err != nil {
		return nil, err
	}
	defer parentDirectory.Close()

	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(parentDirectory.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: securityDescriptor,
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
		return nil, ntStatusErrno(err)
	}

	createdFile := os.NewFile(uintptr(directory), name)
	if createdFile == nil {
		closeErr := windows.CloseHandle(directory)
		return nil, errors.Join(windows.ERROR_INVALID_HANDLE, closeErr)
	}
	createdInfo, statErr := createdFile.Stat()
	closeErr := createdFile.Close()
	if statErr == nil && closeErr == nil {
		return createdInfo, nil
	}
	var cleanupErr error
	if createdInfo != nil {
		cleanupErr = removeCreatedStagingDirectory(parent, name, createdInfo)
	}
	return nil, errors.Join(statErr, closeErr, cleanupErr)
}

func ntStatusErrno(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
