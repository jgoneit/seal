//go:build !linux && !darwin && !windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
)

func publishDirectoryNoReplace(*os.Root, string, string, fs.FileInfo) error {
	return errors.New("atomic no-replace directory publication is unsupported on this operating system")
}

func syncDirectory(*os.Root) error {
	return errors.New("directory synchronization is unsupported on this operating system")
}
