//go:build darwin

package checkrun

import (
	"errors"
	"syscall"
	"time"
)

const processGroupSignalRetryInterval = time.Millisecond

type processGroupSignalCall func(int, syscall.Signal) error

// XNU excludes zombies while iterating a process group, but keeps their group
// alive until they are reaped. A POSIX group signal can therefore report EPERM
// during that interval. Retry only long enough to observe a deliverable signal
// or ESRCH; a persistent permission failure remains an infrastructure error.
func settleProcessGroupSignal(
	processGroupID int,
	signal syscall.Signal,
	initialError error,
) error {
	if !isDarwinProcessGroupSettleSignal(signal) {
		return initialError
	}
	return retryDarwinProcessGroupSignal(
		processGroupID,
		signal,
		initialError,
		int(processTerminateGrace/processGroupSignalRetryInterval),
		time.Sleep,
		syscall.Kill,
	)
}

func isDarwinProcessGroupSettleSignal(signal syscall.Signal) bool {
	return signal == syscall.SIGTERM || signal == syscall.SIGKILL
}

func retryDarwinProcessGroupSignal(
	processGroupID int,
	signal syscall.Signal,
	initialError error,
	maximumAttempts int,
	sleep func(time.Duration),
	signalGroup processGroupSignalCall,
) error {
	err := initialError
	for attempt := 0; errors.Is(err, syscall.EPERM) && attempt < maximumAttempts; attempt++ {
		sleep(processGroupSignalRetryInterval)
		err = signalGroup(-processGroupID, signal)
	}
	return err
}
