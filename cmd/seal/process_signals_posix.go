//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os/signal"
	"syscall"
)

func configureProcessSignals() {
	// Let command handlers observe EPIPE and apply their documented runtime
	// exit instead of terminating the process with SIGPIPE first.
	signal.Ignore(syscall.SIGPIPE)
}
