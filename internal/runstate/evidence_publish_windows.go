//go:build windows

package runstate

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

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

	sourceHandle, err := openForRename(parentHandle, stagingName, windows.FILE_DIRECTORY_FILE)
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
	return renameNoReplace(windows.Handle(source.Fd()), parentHandle, runID)
}

func syncDirectory(*os.Root) error {
	// Every file is flushed before publication. NTFS provides atomic visibility
	// for the handle-relative rename, but Go does not expose a portable
	// directory fsync primitive on Windows.
	return nil
}
