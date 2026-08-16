//go:build darwin

package checkrun

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestDarwinRetriesTransientProcessGroupPermissionError(t *testing.T) {
	responses := []error{syscall.EPERM, syscall.ESRCH}
	var calls int
	var sleeps int

	err := retryDarwinProcessGroupSignal(
		1234,
		syscall.SIGKILL,
		syscall.EPERM,
		len(responses),
		func(duration time.Duration) {
			if duration != processGroupSignalRetryInterval {
				t.Fatalf("retry sleep = %s", duration)
			}
			sleeps++
		},
		func(pid int, signal syscall.Signal) error {
			if pid != -1234 || signal != syscall.SIGKILL {
				t.Fatalf("signal target = (%d, %d)", pid, signal)
			}
			response := responses[calls]
			calls++
			return response
		},
	)
	if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("retry result = %v", err)
	}
	if calls != len(responses) || sleeps != len(responses) {
		t.Fatalf("retry counts = calls:%d sleeps:%d", calls, sleeps)
	}
}

func TestDarwinPreservesPersistentProcessGroupPermissionError(t *testing.T) {
	var calls int
	err := retryDarwinProcessGroupSignal(
		1234,
		syscall.SIGKILL,
		syscall.EPERM,
		3,
		func(time.Duration) {},
		func(int, syscall.Signal) error {
			calls++
			return syscall.EPERM
		},
	)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("retry result = %v", err)
	}
	if calls != 3 {
		t.Fatalf("retry calls = %d", calls)
	}
}

func TestDarwinDoesNotRetryOtherProcessGroupSignals(t *testing.T) {
	err := settleProcessGroupSignal(1234, syscall.SIGTERM, syscall.EPERM)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("signal result = %v", err)
	}
}
