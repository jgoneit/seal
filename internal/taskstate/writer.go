package taskstate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	taskDirectoryMode = 0o755
	taskSnapshotMode  = 0o644
)

func writeTaskSnapshot(
	repository,
	taskID string,
	contents []byte,
	force bool,
	inputPath,
	catalogPath string,
) (returnError error) {
	repositoryRoot, err := os.OpenRoot(repository)
	if err != nil {
		return invalidInput("Could not open the Task snapshot repository root.", err)
	}
	defer repositoryRoot.Close()

	if err := requireRealDirectory(repositoryRoot, ".seal", "Task metadata directory"); err != nil {
		return err
	}
	sealRoot, err := repositoryRoot.OpenRoot(".seal")
	if err != nil {
		return invalidInput("Task metadata directory is unsafe or unavailable.", err)
	}
	defer sealRoot.Close()

	tasksCreated, err := ensureRealDirectory(sealRoot, "tasks", "Task snapshot directory")
	if err != nil {
		return err
	}
	defer func() {
		if returnError != nil && tasksCreated {
			_ = sealRoot.Remove("tasks")
		}
	}()

	tasksRoot, err := sealRoot.OpenRoot("tasks")
	if err != nil {
		return invalidInput("Task snapshot directory is unsafe or unavailable.", err)
	}
	defer tasksRoot.Close()

	destinationName := taskID + ".json"
	destinationPath := filepath.Join(repository, ".seal", "tasks", destinationName)
	if err := inspectTaskDestination(tasksRoot, destinationName, taskID, force); err != nil {
		return err
	}
	if err := rejectTaskWriterAliases(inputPath, catalogPath, destinationPath); err != nil {
		return err
	}

	temporaryName, temporary, err := createTaskTemporary(tasksRoot)
	if err != nil {
		return err
	}
	temporaryExists := true
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if temporaryExists {
			_ = tasksRoot.Remove(temporaryName)
		}
	}()

	if _, err := io.Copy(temporary, bytes.NewReader(contents)); err != nil {
		return invalidInput("Could not write the complete Task snapshot temporary file.", err)
	}
	if err := temporary.Chmod(taskSnapshotMode); err != nil {
		return invalidInput("Could not set deterministic Task snapshot permissions.", err)
	}
	if err := temporary.Sync(); err != nil {
		return invalidInput("Could not synchronize the Task snapshot temporary file.", err)
	}
	if err := temporary.Close(); err != nil {
		temporary = nil
		return invalidInput("Could not close the Task snapshot temporary file.", err)
	}
	temporary = nil

	if force {
		if err := tasksRoot.Rename(temporaryName, destinationName); err != nil {
			return invalidInput(fmt.Sprintf("Could not publish replacement Task '%s'.", taskID), err)
		}
		temporaryExists = false
		return nil
	}

	if err := tasksRoot.Link(temporaryName, destinationName); err != nil {
		if info, statError := tasksRoot.Lstat(destinationName); statError == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return invalidInput(fmt.Sprintf("Task '%s' destination must not be a symlink.", taskID), err)
			}
			return invalidInput(
				fmt.Sprintf("Task '%s' already exists; use --force to replace it.", taskID),
				err,
			)
		}
		return invalidInput(fmt.Sprintf("Could not publish Task '%s' without replacement.", taskID), err)
	}
	if err := tasksRoot.Remove(temporaryName); err == nil {
		temporaryExists = false
	}
	// Link is the no-force commit point. A deferred best-effort cleanup retries
	// removal if the first unlink fails; reporting failure after this point would
	// claim the Task was not created even though the complete destination exists.
	return nil
}

func requireRealDirectory(root *os.Root, name, context string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return invalidInput(context+" is unsafe or unavailable.", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return invalidInput(context+" must be a real directory, not a symlink or non-directory.", nil)
	}
	return nil
}

func ensureRealDirectory(root *os.Root, name, context string) (bool, error) {
	info, err := root.Lstat(name)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, invalidInput(context+" must be a real directory, not a symlink or non-directory.", nil)
		}
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, invalidInput(context+" is unsafe or unavailable.", err)
	}
	if err := root.Mkdir(name, taskDirectoryMode); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return false, invalidInput("Could not create "+context+".", err)
		}
		info, err = root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, invalidInput(context+" became unsafe while it was being created.", err)
		}
		return false, nil
	}
	if err := root.Chmod(name, taskDirectoryMode); err != nil {
		_ = root.Remove(name)
		return false, invalidInput("Could not set deterministic permissions on "+context+".", err)
	}
	info, err = root.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		_ = root.Remove(name)
		return false, invalidInput(context+" became unsafe while it was being created.", err)
	}
	return true, nil
}

func inspectTaskDestination(root *os.Root, destinationName, taskID string, force bool) error {
	info, err := root.Lstat(destinationName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return invalidInput(fmt.Sprintf("Task '%s' destination is unsafe or unavailable.", taskID), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return invalidInput(fmt.Sprintf("Task '%s' destination must not be a symlink.", taskID), nil)
	}
	if !info.Mode().IsRegular() {
		return invalidInput(fmt.Sprintf("Task '%s' destination must be a regular file.", taskID), nil)
	}
	if !force {
		return invalidInput(
			fmt.Sprintf("Task '%s' already exists; use --force to replace it.", taskID),
			nil,
		)
	}
	return nil
}

func rejectTaskWriterAliases(inputPath, catalogPath, destinationPath string) error {
	destination, err := os.Stat(destinationPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return invalidInput("Task snapshot destination is unsafe or unavailable.", err)
	}
	for _, candidate := range []struct {
		path string
		name string
	}{
		{path: inputPath, name: "Task input"},
		{path: catalogPath, name: "Check catalog"},
	} {
		info, statError := os.Stat(candidate.path)
		if statError != nil {
			return invalidInput(candidate.name+" became unavailable before Task publication.", statError)
		}
		if os.SameFile(info, destination) {
			return invalidInput(candidate.name+" must not alias the destination Task snapshot.", nil)
		}
	}
	return nil
}

func createTaskTemporary(root *os.Root) (string, *os.File, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, invalidInput("Could not generate a Task snapshot temporary name.", err)
		}
		name := ".task.tmp-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, invalidInput("Could not create a Task snapshot temporary file.", err)
		}
	}
	return "", nil, invalidInput("Could not allocate a unique Task snapshot temporary file.", nil)
}
