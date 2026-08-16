//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package checkrun

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type posixProcess struct {
	command *exec.Cmd
}

func startProcess(
	argv []string,
	cwd string,
	stdin io.Reader,
	stdout *os.File,
	stderr *os.File,
) (managedProcess, error) {
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = cwd
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &posixProcess{command: command}, nil
}

func (process *posixProcess) wait() processOutcome {
	err := process.command.Wait()
	state := process.command.ProcessState
	if state == nil {
		return processOutcome{err: err}
	}
	waitStatus, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return processOutcome{err: errors.New("check process returned an unsupported wait status")}
	}
	if waitStatus.Signaled() {
		return processOutcome{exitCode: -int64(waitStatus.Signal())}
	}
	return processOutcome{exitCode: int64(waitStatus.ExitStatus())}
}

func (process *posixProcess) terminate(
	outcomeChannel <-chan processOutcome,
	grace time.Duration,
) (processOutcome, error) {
	if err := signalProcessGroup(process.command.Process.Pid, syscall.SIGTERM); err != nil {
		return processOutcome{}, err
	}

	var completed *processOutcome
	timer := time.NewTimer(grace)
	select {
	case outcome := <-outcomeChannel:
		completed = &outcome
		stopTimer(timer)
	case <-timer.C:
	}

	if err := signalProcessGroup(process.command.Process.Pid, syscall.SIGKILL); err != nil {
		return processOutcome{}, err
	}
	if completed != nil {
		return *completed, nil
	}
	return <-outcomeChannel, nil
}

func (process *posixProcess) cleanupAfterExit() error {
	if err := signalProcessGroup(process.command.Process.Pid, syscall.SIGTERM); err != nil {
		return err
	}
	return signalProcessGroup(process.command.Process.Pid, syscall.SIGKILL)
}

func (process *posixProcess) close() error { return nil }

func signalProcessGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
