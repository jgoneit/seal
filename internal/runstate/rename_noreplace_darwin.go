//go:build darwin

package runstate

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames from to to inside root without replacing an
// existing to.
func renameNoReplace(root *os.Root, from, to string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return unix.RenameatxNp(
		int(directory.Fd()), from,
		int(directory.Fd()), to,
		unix.RENAME_EXCL,
	)
}
