//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package checkrun

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestTimeoutTerminatesProcessTree(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SEAL_CHECKRUN_POSIX_HELPER", "1")

	started := time.Now()
	results, err := RunRooted([]Definition{{
		Name:           "timeout tree",
		Argv:           posixHelperArgv("tree-parent-block", childPIDPath),
		Required:       true,
		TimeoutSeconds: big.NewInt(1),
	}}, repository, evidenceRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(started); elapsed < time.Second || elapsed > 5*time.Second {
		t.Fatalf("timeout elapsed = %s", elapsed)
	}
	if !results[0].TimedOut || results[0].Passed {
		t.Fatalf("timeout flags = timed_out:%v passed:%v", results[0].TimedOut, results[0].Passed)
	}
	assertExitCode(t, results[0].ExitCode, -int64(syscall.SIGKILL))
	assertProcessGone(t, readPID(t, childPIDPath))
}

func TestSuccessfulParentCleansBackgroundDescendant(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SEAL_CHECKRUN_POSIX_HELPER", "1")

	results, err := RunRooted([]Definition{{
		Name:     "background child",
		Argv:     posixHelperArgv("tree-parent-exit", childPIDPath),
		Required: true,
	}}, repository, evidenceRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertExitCode(t, results[0].ExitCode, 0)
	if !results[0].Passed || results[0].TimedOut {
		t.Fatalf("result flags = passed:%v timed_out:%v", results[0].Passed, results[0].TimedOut)
	}
	assertProcessGone(t, readPID(t, childPIDPath))
}

func TestContextDeadlineTerminatesProcessTree(t *testing.T) {
	repository := t.TempDir()
	evidenceRoot := openTestRoot(t, privateTempDirectory(t))
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SEAL_CHECKRUN_POSIX_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := RunRootedContext(ctx, []Definition{{
		Name:           "context tree",
		Argv:           posixHelperArgv("tree-parent-block", childPIDPath),
		Required:       true,
		TimeoutSeconds: big.NewInt(MaxTimeoutSeconds),
	}}, repository, evidenceRoot)
	assertResourceLimit(t, err, WallClockResourceLimitMessage)
	assertProcessGone(t, readPID(t, childPIDPath))
}

func TestOutputLimitTerminatesProcessTree(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SEAL_CHECKRUN_POSIX_HELPER", "1")

	_, err := RunRooted([]Definition{{
		Name:           "output tree",
		Argv:           posixHelperArgv("tree-parent-output", childPIDPath),
		Required:       true,
		TimeoutSeconds: big.NewInt(MaxTimeoutSeconds),
	}}, repository, evidenceRoot)
	assertResourceLimit(t, err, fmt.Sprintf(StdoutResourceLimitFormat, 0))
	assertFileSize(t, filepath.Join(evidence, "checks", outputStem(0, "output tree")+".stdout"), MaxStreamOutputBytes)
	assertProcessGone(t, readPID(t, childPIDPath))
}

func TestEscapedDescendantHoldingPipesDoesNotBlockCollectorCleanup(t *testing.T) {
	t.Setenv("SEAL_CHECKRUN_POSIX_HELPER", "1")
	tests := []struct {
		name           string
		mode           string
		contextTimeout time.Duration
		checkTimeout   int64
		message        string
	}{
		{
			name:         "output limit",
			mode:         "escape-parent-output",
			checkTimeout: MaxTimeoutSeconds,
			message:      fmt.Sprintf(StdoutResourceLimitFormat, 0),
		},
		{
			name:           "context deadline",
			mode:           "escape-parent-block",
			contextTimeout: time.Second,
			checkTimeout:   MaxTimeoutSeconds,
			message:        WallClockResourceLimitMessage,
		},
		{
			name:         "per-check timeout",
			mode:         "escape-parent-block",
			checkTimeout: 1,
			message:      fmt.Sprintf(PipeDrainResourceLimitFormat, 0),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			evidenceRoot := openTestRoot(t, privateTempDirectory(t))
			childPIDPath := filepath.Join(t.TempDir(), "escaped-child.pid")
			ctx := context.Background()
			cancel := func() {}
			if test.contextTimeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, test.contextTimeout)
			}
			defer cancel()

			done := make(chan error, 1)
			go func() {
				_, err := RunRootedContext(ctx, []Definition{{
					Name:           "escaped pipe holder",
					Argv:           posixHelperArgv(test.mode, childPIDPath),
					Required:       true,
					TimeoutSeconds: big.NewInt(test.checkTimeout),
				}}, repository, evidenceRoot)
				done <- err
			}()

			select {
			case err := <-done:
				pid := readPID(t, childPIDPath)
				killEscapedProcess(t, pid)
				assertResourceLimit(t, err, test.message)
			case <-time.After(4 * time.Second):
				pid := readPID(t, childPIDPath)
				killEscapedProcess(t, pid)
				<-done
				t.Fatal("runner blocked on pipes retained by escaped descendant")
			}
		})
	}
}

func TestPOSIXCheckrunHelperProcess(t *testing.T) {
	if os.Getenv("SEAL_CHECKRUN_POSIX_HELPER") != "1" {
		return
	}
	arguments := helperArguments()
	if len(arguments) != 2 {
		os.Exit(92)
	}
	switch arguments[0] {
	case "tree-parent-block", "tree-parent-exit", "tree-parent-output":
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestPOSIXCheckrunHelperProcess$",
			"--",
			"tree-child",
			arguments[1],
		)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(91)
		}
		if err := os.WriteFile(arguments[1], []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
			os.Exit(90)
		}
		if arguments[0] == "tree-parent-exit" {
			os.Exit(0)
		}
		if arguments[0] == "tree-parent-output" {
			remaining := MaxStreamOutputBytes + 1
			chunk := make([]byte, 64*1024)
			for remaining > 0 {
				length := int64(len(chunk))
				if remaining < length {
					length = remaining
				}
				written, err := os.Stdout.Write(chunk[:int(length)])
				if err != nil {
					os.Exit(88)
				}
				remaining -= int64(written)
			}
			os.Exit(0)
		}
		signalIgnoreTERM()
		for {
			time.Sleep(time.Hour)
		}
	case "tree-child":
		signalIgnoreTERM()
		for {
			time.Sleep(time.Hour)
		}
	case "escape-parent-block", "escape-parent-output":
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestPOSIXCheckrunHelperProcess$",
			"--",
			"escape-child",
			arguments[1],
		)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(88)
		}
		_ = command.Process.Release()
		if !waitForPOSIXHelperPath(arguments[1]) {
			os.Exit(87)
		}
		if arguments[0] == "escape-parent-output" {
			remaining := MaxStreamOutputBytes + 1
			chunk := make([]byte, 64*1024)
			for remaining > 0 {
				length := int64(len(chunk))
				if remaining < length {
					length = remaining
				}
				written, err := os.Stdout.Write(chunk[:int(length)])
				if err != nil {
					os.Exit(86)
				}
				remaining -= int64(written)
			}
			os.Exit(0)
		}
		signalIgnoreTERM()
		for {
			time.Sleep(time.Hour)
		}
	case "escape-child":
		if _, err := syscall.Setsid(); err != nil {
			os.Exit(85)
		}
		if err := os.WriteFile(arguments[1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(84)
		}
		signalIgnoreTERM()
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(89)
	}
}

func posixHelperArgv(mode string, arguments ...string) []string {
	argv := []string{os.Args[0], "-test.run=^TestPOSIXCheckrunHelperProcess$", "--", mode}
	return append(argv, arguments...)
}

func signalIgnoreTERM() {
	signal.Ignore(syscall.SIGTERM)
}

func waitForPOSIXHelperPath(path string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func killEscapedProcess(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("kill escaped process %d: %v", pid, err)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	contents := readFile(t, path)
	pid, err := strconv.Atoi(contents)
	if err != nil {
		t.Fatalf("pid %q: %v", contents, err)
	}
	return pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe process %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("background process %d survived check cleanup", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
