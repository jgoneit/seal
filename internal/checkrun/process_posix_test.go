//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package checkrun

import (
	"errors"
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

func TestPOSIXCheckrunHelperProcess(t *testing.T) {
	if os.Getenv("SEAL_CHECKRUN_POSIX_HELPER") != "1" {
		return
	}
	arguments := helperArguments()
	if len(arguments) != 2 {
		os.Exit(92)
	}
	switch arguments[0] {
	case "tree-parent-block", "tree-parent-exit":
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
		signalIgnoreTERM()
		for {
			time.Sleep(time.Hour)
		}
	case "tree-child":
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
