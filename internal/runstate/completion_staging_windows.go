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

func createPrivateCompletionTempWithHooks(root *os.Root, name string, hooks completionTempHooks) (*os.File, fs.FileInfo, error) {
	if err := validateRelativeName(name); err != nil {
		return nil, nil, err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	descriptor, err := privateSecurityDescriptor(&pinner, windows.NO_INHERITANCE)
	if err != nil {
		return nil, nil, err
	}

	directory, err := root.Open(".")
	if err != nil {
		return nil, nil, err
	}
	defer directory.Close()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(directory.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocationSize int64
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE,
		attributes,
		&status,
		&allocationSize,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|
			windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return nil, nil, ntStatusErrno(err)
	}
	newFile := hooks.newFile
	if newFile == nil {
		newFile = os.NewFile
	}
	file := newFile(uintptr(handle), name)
	if file == nil {
		cleanupErr := cleanupCompletionHandle(handle)
		return nil, nil, errors.Join(windows.ERROR_INVALID_HANDLE, cleanupErr)
	}
	stat := hooks.stat
	if stat == nil {
		stat = func(file *os.File) (fs.FileInfo, error) { return file.Stat() }
	}
	info, err := stat(file)
	if err != nil {
		cleanupErr := cleanupCompletionFile(file)
		return nil, nil, errors.Join(err, cleanupErr)
	}
	return file, info, nil
}

func cleanupCompletionFile(file *os.File) error {
	handle := windows.Handle(file.Fd())
	var status windows.IO_STATUS_BLOCK
	deleteFile := byte(1)
	deleteErr := windows.NtSetInformationFile(
		handle,
		&status,
		&deleteFile,
		uint32(unsafe.Sizeof(deleteFile)),
		windows.FileDispositionInformation,
	)
	return errors.Join(ntStatusErrno(deleteErr), file.Close())
}

func cleanupCompletionHandle(handle windows.Handle) error {
	var status windows.IO_STATUS_BLOCK
	deleteFile := byte(1)
	deleteErr := windows.NtSetInformationFile(
		handle,
		&status,
		&deleteFile,
		uint32(unsafe.Sizeof(deleteFile)),
		windows.FileDispositionInformation,
	)
	return errors.Join(ntStatusErrno(deleteErr), windows.CloseHandle(handle))
}
