//go:build windows

package runstate

import (
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func publishDirectoryNoReplace(
	parent *os.Root,
	stagingName string,
	runID string,
	expected fs.FileInfo,
) error {
	if err := validateRelativeName(stagingName); err != nil {
		return err
	}
	if err := validateRelativeName(runID); err != nil {
		return err
	}
	parentDirectory, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer parentDirectory.Close()
	parentHandle := windows.Handle(parentDirectory.Fd())

	sourceHandle, err := openDirectoryForRename(parentHandle, stagingName)
	if err != nil {
		return err
	}
	source := os.NewFile(uintptr(sourceHandle), stagingName)
	if source == nil {
		_ = windows.CloseHandle(sourceHandle)
		return windows.ERROR_INVALID_HANDLE
	}
	defer source.Close()
	actual, err := source.Stat()
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, actual) {
		return windows.ERROR_FILE_INVALID
	}

	name, err := windows.UTF16FromString(runID)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + len(name)*2
	buffer := make([]byte, bufferSize)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.ReplaceIfExists = 0
	information.RootDirectory = parentHandle
	information.FileNameLength = uint32(len(name) * 2)
	destination := unsafe.Slice(&information.FileName[0], len(name))
	copy(destination, name)

	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		windows.Handle(source.Fd()),
		&status,
		&buffer[0],
		uint32(len(buffer)),
		windows.FileRenameInformation,
	)
}

func openDirectoryForRename(parent windows.Handle, name string) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	var allocationSize int64
	err = windows.NtCreateFile(
		&handle,
		windows.DELETE|windows.SYNCHRONIZE,
		attributes,
		&status,
		&allocationSize,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|
			windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return 0, err
	}
	return handle, nil
}

func syncDirectory(*os.Root) error {
	// Every file is flushed before publication. NTFS provides atomic visibility
	// for the handle-relative rename, but Go does not expose a portable
	// directory fsync primitive on Windows.
	return nil
}
