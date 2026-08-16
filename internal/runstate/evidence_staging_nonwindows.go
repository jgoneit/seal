//go:build !windows

package runstate

import "os"

func createPrivateStagingDirectory(parent *os.Root, name string) error {
	if err := parent.Mkdir(name, 0o700); err != nil {
		return err
	}
	if err := parent.Chmod(name, 0o700); err != nil {
		_ = parent.RemoveAll(name)
		return err
	}
	return nil
}
