//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris && !windows

package sourceobs

import (
	"errors"
	"os/exec"
)

// runGitCommand fails closed where the platform cannot give Seal bounded
// ownership of Git's descendant process tree.
func runGitCommand(_ *exec.Cmd) error {
	return errors.New("managed Git process-tree ownership is unsupported on this operating system")
}
