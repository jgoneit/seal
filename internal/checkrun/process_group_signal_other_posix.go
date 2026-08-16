//go:build dragonfly || freebsd || linux || netbsd || openbsd

package checkrun

import "syscall"

func settleProcessGroupSignal(
	_ int,
	_ syscall.Signal,
	initialError error,
) error {
	return initialError
}
