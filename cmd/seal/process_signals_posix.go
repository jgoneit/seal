//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os"
	"os/signal"
	"syscall"
)

var brokenPipeSignals = make(chan os.Signal, 1)

func configureProcessSignals() {
	// Let command handlers observe EPIPE and apply their documented runtime
	// exit instead of terminating the process with SIGPIPE first. Notify uses a
	// caught handler rather than SIG_IGN, so executed checks regain the normal
	// SIGPIPE disposition across exec.
	signal.Notify(brokenPipeSignals, syscall.SIGPIPE)
}
