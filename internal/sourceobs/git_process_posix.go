//go:build aix || android || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package sourceobs

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

const gitProcessGroupSettleLimit = 200 * time.Millisecond

// runGitCommand gives each Git invocation a private session. CommandContext's
// cancellation hook then kills the entire process group instead of only Git's
// root process. A descendant that deliberately starts a new session can escape
// portable process-group ownership; exec's bounded pipe wait still lets Verify
// return after cancellation in that case.
func runGitCommand(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		groupError := killGitProcessGroup(command.Process.Pid)
		if groupError == nil {
			return nil
		}
		if errors.Is(groupError, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		rootError := command.Process.Kill()
		if errors.Is(rootError, os.ErrProcessDone) {
			rootError = nil
		}
		return errors.Join(groupError, rootError)
	}
	return command.Run()
}

func killGitProcessGroup(processGroupID int) error {
	deadline := time.Now().Add(gitProcessGroupSettleLimit)
	for {
		err := syscall.Kill(-processGroupID, syscall.SIGKILL)
		if runtime.GOOS != "darwin" || !errors.Is(err, syscall.EPERM) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}
