//go:build windows

package sourceobs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runGitCommand starts Git suspended, binds it to a kill-on-close Job, and
// resumes it only after the binding succeeds. This removes the spawn-before-Job
// race and lets CommandContext cancellation terminate ordinary descendants.
func runGitCommand(command *exec.Cmd) error {
	job, err := createGitJob()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)

	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED

	var assigned atomic.Bool
	var canceled atomic.Bool
	command.Cancel = func() error {
		canceled.Store(true)
		var jobError error
		if assigned.Load() {
			jobError = windows.TerminateJobObject(job, 1)
			if jobError == nil {
				return nil
			}
		}
		if command.Process == nil {
			if jobError != nil {
				return jobError
			}
			return os.ErrProcessDone
		}
		processError := command.Process.Kill()
		if errors.Is(processError, os.ErrProcessDone) {
			processError = nil
		}
		return errors.Join(jobError, processError)
	}

	if err := command.Start(); err != nil {
		return err
	}
	failStarted := func(preparationError error) error {
		terminationError := terminateGitJobAndRoot(job, command.Process)
		waitError := command.Wait()
		if preparationError != nil {
			// Wait only reaps the failed-start process here. Do not expose its
			// ExitError: callers treat Git exit statuses as command outcomes,
			// while a Job setup failure must always fail closed.
			return errors.Join(preparationError, terminationError)
		}
		return errors.Join(preparationError, terminationError, waitError)
	}

	if err := assignGitProcess(job, command.Process); err != nil {
		return failStarted(err)
	}
	assigned.Store(true)
	if canceled.Load() {
		return failStarted(nil)
	}

	thread, err := openGitPrimaryThread(uint32(command.Process.Pid))
	if err != nil {
		return failStarted(err)
	}
	_, resumeError := windows.ResumeThread(thread)
	closeError := windows.CloseHandle(thread)
	if resumeError != nil || closeError != nil {
		return failStarted(errors.Join(resumeError, closeError))
	}

	waitError := command.Wait()
	cleanupError := windows.TerminateJobObject(job, 1)
	if waitError != nil {
		return waitError
	}
	return cleanupError
}

func createGitJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
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

func assignGitProcess(job windows.Handle, process *os.Process) error {
	var assignError error
	handleError := process.WithHandle(func(handle uintptr) {
		assignError = windows.AssignProcessToJobObject(job, windows.Handle(handle))
	})
	return errors.Join(handleError, assignError)
}

func openGitPrimaryThread(processID uint32) (windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return 0, err
	}
	for {
		if entry.OwnerProcessID == processID {
			return windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		err := windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return 0, fmt.Errorf("could not locate the suspended Git primary thread")
		}
		if err != nil {
			return 0, err
		}
	}
}

func terminateGitJobAndRoot(job windows.Handle, process *os.Process) error {
	jobError := windows.TerminateJobObject(job, 1)
	var processError error
	if process != nil {
		processError = process.Kill()
		if errors.Is(processError, os.ErrProcessDone) {
			processError = nil
		}
	}
	return errors.Join(jobError, processError)
}
