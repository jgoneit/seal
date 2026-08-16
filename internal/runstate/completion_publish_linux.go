//go:build linux

package runstate

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func publishCompletionNoReplace(root *os.Root, stagingName, destinationName string, expected fs.FileInfo) error {
	if err := validateRelativeName(destinationName); err != nil {
		return err
	}
	if err := completionTempIdentity(root, stagingName, expected); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return unix.Renameat2(
		int(directory.Fd()), stagingName,
		int(directory.Fd()), destinationName,
		unix.RENAME_NOREPLACE,
	)
}
