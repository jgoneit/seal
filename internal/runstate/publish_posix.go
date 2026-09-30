//go:build linux || darwin

package runstate

import (
	"io/fs"
	"os"
)

func publishDirectoryNoReplace(parent *os.Root, stagingName, runID string, _ fs.FileInfo) error {
	if err := validateRelativeName(stagingName); err != nil {
		return err
	}
	if err := validateRelativeName(runID); err != nil {
		return err
	}
	return renameNoReplace(parent, stagingName, runID)
}

func publishCompletionNoReplace(root *os.Root, stagingName, destinationName string, expected fs.FileInfo) error {
	if err := validateRelativeName(destinationName); err != nil {
		return err
	}
	if err := completionTempIdentity(root, stagingName, expected); err != nil {
		return err
	}
	return renameNoReplace(root, stagingName, destinationName)
}

func syncDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
