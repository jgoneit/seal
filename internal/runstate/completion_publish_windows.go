//go:build windows

package runstate

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func publishCompletionNoReplace(
	root *os.Root,
	stagingName string,
	destinationName string,
	expected fs.FileInfo,
) error {
	if err := validateRelativeName(stagingName); err != nil {
		return err
	}
	if err := validateRelativeName(destinationName); err != nil {
		return err
	}
	rootDirectory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer rootDirectory.Close()
	rootHandle := windows.Handle(rootDirectory.Fd())

	sourceHandle, err := openForRename(rootHandle, stagingName, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return ntStatusErrno(err)
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
	if expected == nil || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return windows.ERROR_FILE_INVALID
	}
	return ntStatusErrno(renameNoReplace(windows.Handle(source.Fd()), rootHandle, destinationName))
}
