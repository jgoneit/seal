//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package checkrun

import (
	"errors"
	"io"
	"os"
)

func startProcess(
	[]string,
	string,
	io.Reader,
	*os.File,
	*os.File,
) (managedProcess, error) {
	return nil, errors.New("check process management is unsupported on this operating system")
}
