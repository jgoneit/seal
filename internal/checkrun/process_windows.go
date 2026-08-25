//go:build windows

package checkrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcess struct {
	job         windows.Handle
	process     windows.Handle
	closeJob    sync.Once
	closeHandle sync.Once
	jobError    error
	handleError error
}

func startProcess(
	argv []string,
	cwd string,
	stdin io.Reader,
	stdout *os.File,
	stderr *os.File,
) (managedProcess, error) {
	stdinFile, ok := stdin.(*os.File)
	if !ok {
		return nil, fmt.Errorf("check stdin is not an operating-system file")
	}
	job, err := createWindowsJob()
	if err != nil {
		return nil, err
	}

	process := &windowsProcess{job: job}
	created := false
	var processInformation windows.ProcessInformation
	defer func() {
		if created {
			_ = windows.CloseHandle(processInformation.Thread)
		}
	}()

	stdinHandle, err := inheritableHandle(stdinFile)
	if err != nil {
		_ = process.close()
		return nil, err
	}
	defer windows.CloseHandle(stdinHandle)
	stdoutHandle, err := inheritableHandle(stdout)
	if err != nil {
		_ = process.close()
		return nil, err
	}
	defer windows.CloseHandle(stdoutHandle)
	stderrHandle, err := inheritableHandle(stderr)
	if err != nil {
		_ = process.close()
		return nil, err
	}
	defer windows.CloseHandle(stderrHandle)
	attributeList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		_ = process.close()
		return nil, err
	}
	defer attributeList.Delete()
	inheritedHandles := []windows.Handle{stdinHandle, stdoutHandle, stderrHandle}
	if err := attributeList.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inheritedHandles[0]),
		uintptr(len(inheritedHandles))*unsafe.Sizeof(inheritedHandles[0]),
	); err != nil {
		_ = process.close()
		return nil, err
	}

	executable, err := exec.LookPath(argv[0])
	if err != nil {
		_ = process.close()
		return nil, err
	}
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		_ = process.close()
		return nil, err
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		_ = process.close()
		return nil, err
	}
	currentDirectory, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		_ = process.close()
		return nil, err
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  stdinHandle,
			StdOutput: stdoutHandle,
			StdErr:    stderrHandle,
		},
		ProcThreadAttributeList: attributeList.List(),
	}
	creationFlags := uint32(
		windows.CREATE_SUSPENDED |
			windows.CREATE_NEW_PROCESS_GROUP |
			windows.EXTENDED_STARTUPINFO_PRESENT,
	)
	if err := windows.CreateProcess(
		application,
		commandLine,
		nil,
		nil,
		true,
		creationFlags,
		nil,
		currentDirectory,
		&startup.StartupInfo,
		&processInformation,
	); err != nil {
		_ = process.close()
		return nil, err
	}
	created = true
	process.process = processInformation.Process

	if err := windows.AssignProcessToJobObject(job, processInformation.Process); err != nil {
		_ = windows.TerminateProcess(processInformation.Process, 1)
		_, _ = windows.WaitForSingleObject(processInformation.Process, windows.INFINITE)
		_ = process.close()
		return nil, err
	}
	// The primary thread is still suspended here. User code cannot run until
	// the root process is a member of the configured kill-on-close Job.
	if _, err := windows.ResumeThread(processInformation.Thread); err != nil {
		_ = process.closeJobHandle()
		_ = windows.TerminateProcess(processInformation.Process, 1)
		_, _ = windows.WaitForSingleObject(processInformation.Process, windows.INFINITE)
		_ = process.close()
		return nil, err
	}
	return process, nil
}

func createWindowsJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func inheritableHandle(file *os.File) (windows.Handle, error) {
	currentProcess := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		currentProcess,
		windows.Handle(file.Fd()),
		currentProcess,
		&duplicate,
		0,
		true,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return 0, err
	}
	return duplicate, nil
}

func (process *windowsProcess) wait() processOutcome {
	result, err := windows.WaitForSingleObject(process.process, windows.INFINITE)
	if err != nil {
		return processOutcome{err: err}
	}
	if result != windows.WAIT_OBJECT_0 {
		return processOutcome{err: fmt.Errorf("unexpected Windows process wait result %#x", result)}
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.process, &exitCode); err != nil {
		return processOutcome{err: err}
	}
	return processOutcome{exitCode: int64(exitCode)}
}

func (process *windowsProcess) terminate(
	outcomeChannel <-chan processOutcome,
	grace time.Duration,
) (processOutcome, error) {
	cleanupError := process.closeJobHandle()
	timer := time.NewTimer(grace)
	select {
	case outcome := <-outcomeChannel:
		stopTimer(timer)
		return outcome, cleanupError
	case <-timer.C:
	}
	if err := windows.TerminateProcess(process.process, 1); err != nil {
		cleanupError = errors.Join(cleanupError, err)
	}
	// The root process is retained until its wait outcome is consumed. This
	// keeps the timeout path from abandoning a goroutine in WaitForSingleObject
	// even when Job closure or the direct termination fallback reports an error.
	return <-outcomeChannel, cleanupError
}

func (process *windowsProcess) cleanupAfterExit() error {
	return process.closeJobHandle()
}

func (process *windowsProcess) close() error {
	_ = process.closeJobHandle()
	process.closeHandle.Do(func() {
		if process.process != 0 {
			process.handleError = windows.CloseHandle(process.process)
			process.process = 0
		}
	})
	if process.jobError != nil {
		return process.jobError
	}
	return process.handleError
}

func (process *windowsProcess) closeJobHandle() error {
	process.closeJob.Do(func() {
		if process.job != 0 {
			process.jobError = windows.CloseHandle(process.job)
			process.job = 0
		}
	})
	return process.jobError
}
