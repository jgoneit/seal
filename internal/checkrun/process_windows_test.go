//go:build windows

package checkrun

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTimeoutTerminatesProcessTree(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("SEAL_CHECKRUN_WINDOWS_TREE_HELPER", "1")

	runDone := make(chan windowsTreeRunResult, 1)
	go func() {
		results, err := RunRooted([]Definition{{
			Name:           "timeout tree",
			Argv:           windowsTreeHelperArgv("tree-parent-block", childPIDPath),
			Required:       true,
			TimeoutSeconds: big.NewInt(3),
		}}, repository, evidenceRoot)
		runDone <- windowsTreeRunResult{results: results, err: err}
	}()

	child := openWindowsHelperProcess(t, childPIDPath)
	defer windows.CloseHandle(child)
	outcome := waitForWindowsTreeRun(t, runDone)
	if outcome.err != nil {
		t.Fatalf("Run: %v", outcome.err)
	}
	if len(outcome.results) != 1 {
		t.Fatalf("result count = %d, want 1", len(outcome.results))
	}
	result := outcome.results[0]
	if !result.TimedOut || result.Passed {
		t.Fatalf("timeout flags = timed_out:%v passed:%v", result.TimedOut, result.Passed)
	}
	if result.ExitCode == nil {
		t.Fatal("timed-out process has no exit code")
	}
	assertWindowsProcessExited(t, child)
}

func TestSuccessfulParentCleansBackgroundDescendant(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	fixtureDirectory := t.TempDir()
	childPIDPath := filepath.Join(fixtureDirectory, "child.pid")
	releasePath := filepath.Join(fixtureDirectory, "release")
	t.Setenv("SEAL_CHECKRUN_WINDOWS_TREE_HELPER", "1")

	runDone := make(chan windowsTreeRunResult, 1)
	go func() {
		results, err := RunRooted([]Definition{{
			Name:           "background child",
			Argv:           windowsTreeHelperArgv("tree-parent-exit", childPIDPath, releasePath),
			Required:       true,
			TimeoutSeconds: big.NewInt(10),
		}}, repository, evidenceRoot)
		runDone <- windowsTreeRunResult{results: results, err: err}
	}()

	child := openWindowsHelperProcess(t, childPIDPath)
	defer windows.CloseHandle(child)
	if err := os.WriteFile(releasePath, []byte("exit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome := waitForWindowsTreeRun(t, runDone)
	if outcome.err != nil {
		t.Fatalf("Run: %v", outcome.err)
	}
	if len(outcome.results) != 1 {
		t.Fatalf("result count = %d, want 1", len(outcome.results))
	}
	result := outcome.results[0]
	assertExitCode(t, result.ExitCode, 0)
	if !result.Passed || result.TimedOut {
		t.Fatalf("result flags = passed:%v timed_out:%v", result.Passed, result.TimedOut)
	}
	assertWindowsProcessExited(t, child)
}

func TestRunDoesNotInheritUnlistedHandle(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	t.Setenv("SEAL_CHECKRUN_WINDOWS_HELPER", "1")

	sentinel, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sentinel)
	if err := windows.SetHandleInformation(
		sentinel,
		windows.HANDLE_FLAG_INHERIT,
		windows.HANDLE_FLAG_INHERIT,
	); err != nil {
		t.Fatal(err)
	}
	evidenceRoot := openTestRoot(t, evidence)

	results, err := RunRooted([]Definition{{
		Name:     "handle probe",
		Argv:     windowsHelperArgv("signal-event", strconv.FormatUint(uint64(sentinel), 10)),
		Required: true,
	}}, repository, evidenceRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertExitCode(t, results[0].ExitCode, 0)
	if got := readFile(t, filepath.Join(evidence, filepath.FromSlash(results[0].StdoutPath))); got != "attempted\n" {
		t.Fatalf("helper stdout = %q", got)
	}

	result, err := windows.WaitForSingleObject(sentinel, 0)
	if err != nil {
		t.Fatal(err)
	}
	switch result {
	case uint32(windows.WAIT_TIMEOUT):
		// The child attempted to signal the numeric handle, but the parent's
		// anonymous sentinel was not present in the child's handle table.
	case windows.WAIT_OBJECT_0:
		t.Fatal("unlisted inheritable handle reached the child process")
	default:
		t.Fatalf("unexpected sentinel wait result %#x", result)
	}
}

func TestWindowsHandleProbeHelperProcess(t *testing.T) {
	if os.Getenv("SEAL_CHECKRUN_WINDOWS_HELPER") != "1" {
		return
	}
	arguments := helperArguments()
	if len(arguments) != 2 || arguments[0] != "signal-event" {
		os.Exit(92)
	}
	handle, err := strconv.ParseUint(arguments[1], 10, 64)
	if err != nil {
		os.Exit(91)
	}
	_ = windows.SetEvent(windows.Handle(uintptr(handle)))
	_, _ = fmt.Fprintln(os.Stdout, "attempted")
	os.Exit(0)
}

func TestWindowsProcessTreeHelperProcess(t *testing.T) {
	if os.Getenv("SEAL_CHECKRUN_WINDOWS_TREE_HELPER") != "1" {
		return
	}
	arguments := helperArguments()
	if len(arguments) == 0 {
		os.Exit(89)
	}
	switch arguments[0] {
	case "tree-parent-block", "tree-parent-exit":
		if len(arguments) < 2 {
			os.Exit(88)
		}
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestWindowsProcessTreeHelperProcess$",
			"--",
			"tree-child",
			arguments[1],
		)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		// Do not request CREATE_BREAKAWAY_FROM_JOB here. The compatibility
		// contract permits deliberate breakaway, while these tests cover the
		// ordinary descendant path that inherits the managed Job.
		if err := command.Start(); err != nil {
			os.Exit(87)
		}
		if !waitForWindowsHelperPath(arguments[1]) {
			_ = command.Process.Kill()
			os.Exit(86)
		}
		_ = command.Process.Release()
		if arguments[0] == "tree-parent-exit" {
			if len(arguments) != 3 || !waitForWindowsHelperPath(arguments[2]) {
				os.Exit(85)
			}
			os.Exit(0)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "tree-child":
		if len(arguments) != 2 {
			os.Exit(84)
		}
		if err := os.WriteFile(arguments[1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(83)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(82)
	}
}

func windowsHelperArgv(mode string, arguments ...string) []string {
	argv := []string{os.Args[0], "-test.run=^TestWindowsHandleProbeHelperProcess$", "--", mode}
	return append(argv, arguments...)
}

func windowsTreeHelperArgv(mode string, arguments ...string) []string {
	argv := []string{os.Args[0], "-test.run=^TestWindowsProcessTreeHelperProcess$", "--", mode}
	return append(argv, arguments...)
}

type windowsTreeRunResult struct {
	results []Result
	err     error
}

func openWindowsHelperProcess(t *testing.T, pidPath string) windows.Handle {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		contents, err := os.ReadFile(pidPath)
		if err == nil {
			pid, parseError := strconv.ParseUint(strings.TrimSpace(string(contents)), 10, 32)
			if parseError == nil && pid != 0 {
				handle, openError := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
				if openError != nil {
					t.Fatalf("open helper process %d: %v", pid, openError)
				}
				return handle
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper pid: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper process did not publish a pid at %s", pidPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForWindowsTreeRun(t *testing.T, done <-chan windowsTreeRunResult) windowsTreeRunResult {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(15 * time.Second):
		t.Fatal("managed Windows process tree did not finish")
		return windowsTreeRunResult{}
	}
}

func assertWindowsProcessExited(t *testing.T, process windows.Handle) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := windows.WaitForSingleObject(process, 50)
		if err != nil {
			t.Fatalf("wait for descendant: %v", err)
		}
		switch result {
		case windows.WAIT_OBJECT_0:
			return
		case uint32(windows.WAIT_TIMEOUT):
			if time.Now().After(deadline) {
				t.Fatal("background descendant survived check cleanup")
			}
		default:
			t.Fatalf("unexpected descendant wait result %#x", result)
		}
	}
}

func waitForWindowsHelperPath(path string) bool {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		} else if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
