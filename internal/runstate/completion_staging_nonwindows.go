//go:build !windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
)

func createPrivateCompletionTempWithHooks(root *os.Root, name string, hooks completionTempHooks) (*os.File, fs.FileInfo, error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, nil, err
	}

	// Bind cleanup to the descriptor before chmod. If this first stat fails,
	// cleanup retries descriptor stat only to obtain an identity; it never
	// removes the path by name when that identity cannot be recovered. That
	// fail-closed case can leave residue, but cannot remove an unrelated object
	// that replaced the generated name.
	stat := hooks.stat
	if stat == nil {
		stat = func(file *os.File) (fs.FileInfo, error) { return file.Stat() }
	}
	createdInfo, err := stat(file)
	if err != nil {
		cleanupErr := cleanupOpenedCompletionTemp(root, name, file, nil)
		return nil, nil, errors.Join(err, cleanupErr)
	}
	if !createdInfo.Mode().IsRegular() {
		cleanupErr := cleanupOpenedCompletionTemp(root, name, file, createdInfo)
		return nil, nil, errors.Join(errors.New("created completion staging object is not a regular file"), cleanupErr)
	}

	chmod := hooks.chmod
	if chmod == nil {
		chmod = func(file *os.File, mode fs.FileMode) error { return file.Chmod(mode) }
	}
	if err := chmod(file, 0o600); err != nil {
		cleanupErr := cleanupOpenedCompletionTemp(root, name, file, createdInfo)
		return nil, nil, errors.Join(err, cleanupErr)
	}
	info, err := file.Stat()
	if err != nil {
		cleanupErr := cleanupOpenedCompletionTemp(root, name, file, createdInfo)
		return nil, nil, errors.Join(err, cleanupErr)
	}
	if !info.Mode().IsRegular() || !os.SameFile(createdInfo, info) {
		cleanupErr := cleanupOpenedCompletionTemp(root, name, file, createdInfo)
		return nil, nil, errors.Join(errors.New("completion staging descriptor identity changed after creation"), cleanupErr)
	}
	return file, info, nil
}

func cleanupOpenedCompletionTemp(root *os.Root, name string, file *os.File, expected fs.FileInfo) error {
	if expected == nil {
		var err error
		expected, err = file.Stat()
		if err != nil {
			return errors.Join(err, file.Close())
		}
	}
	named, inspectErr := root.Lstat(name)
	var cleanupErr error
	switch {
	case inspectErr != nil:
		cleanupErr = inspectErr
	case named.Mode()&os.ModeSymlink != 0 || !named.Mode().IsRegular() || !os.SameFile(expected, named):
		cleanupErr = errors.New("completion staging destination identity changed before post-create cleanup")
	default:
		cleanupErr = root.Remove(name)
	}
	return errors.Join(cleanupErr, file.Close())
}
