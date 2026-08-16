//go:build darwin

package checkrun

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestDarwinRetriesTransientProcessGroupPermissionError(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "SIGTERM", signal: syscall.SIGTERM},
		{name: "SIGKILL", signal: syscall.SIGKILL},
	} {
		t.Run(test.name, func(t *testing.T) {
			responses := []error{syscall.EPERM, syscall.ESRCH}
			var calls int
			var sleeps int

			err := retryDarwinProcessGroupSignal(
				1234,
				test.signal,
				syscall.EPERM,
				len(responses),
				func(duration time.Duration) {
					if duration != processGroupSignalRetryInterval {
						t.Fatalf("retry sleep = %s", duration)
					}
					sleeps++
				},
				func(pid int, signal syscall.Signal) error {
					if pid != -1234 || signal != test.signal {
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
		})
	}
}

func TestDarwinPreservesPersistentProcessGroupPermissionError(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "SIGTERM", signal: syscall.SIGTERM},
		{name: "SIGKILL", signal: syscall.SIGKILL},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			err := retryDarwinProcessGroupSignal(
				1234,
				test.signal,
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
		})
	}
}

func TestDarwinSettlesOnlyTerminationProcessGroupSignals(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal syscall.Signal
		want   bool
	}{
		{name: "SIGTERM", signal: syscall.SIGTERM, want: true},
		{name: "SIGKILL", signal: syscall.SIGKILL, want: true},
		{name: "SIGINT", signal: syscall.SIGINT, want: false},
		{name: "SIGHUP", signal: syscall.SIGHUP, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isDarwinProcessGroupSettleSignal(test.signal); got != test.want {
				t.Fatalf("settle signal = %t, want %t", got, test.want)
			}
		})
	}
}
