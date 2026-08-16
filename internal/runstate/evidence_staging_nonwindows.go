//go:build !windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
)

func createPrivateStagingDirectory(parent *os.Root, name string) (fs.FileInfo, error) {
	if err := parent.Mkdir(name, 0o700); err != nil {
		return nil, err
	}
	if err := parent.Chmod(name, 0o700); err != nil {
		_ = parent.RemoveAll(name)
		return nil, err
	}
	created, err := parent.Lstat(name)
	if err != nil {
		_ = parent.RemoveAll(name)
		return nil, err
	}
	if created.Mode()&os.ModeSymlink != 0 || !created.IsDir() {
		return nil, errors.New("created Evidence staging path is not a real directory")
	}
	return created, nil
}
