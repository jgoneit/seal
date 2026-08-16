//go:build linux

package runstate

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func publishDirectoryNoReplace(parent *os.Root, stagingName, runID string, _ fs.FileInfo) error {
	if err := validateRelativeName(stagingName); err != nil {
		return err
	}
	if err := validateRelativeName(runID); err != nil {
		return err
	}
	directory, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return unix.Renameat2(
		int(directory.Fd()), stagingName,
		int(directory.Fd()), runID,
		unix.RENAME_NOREPLACE,
	)
}

func syncDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
