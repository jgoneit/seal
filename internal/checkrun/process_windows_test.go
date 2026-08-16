//go:build windows

package checkrun

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

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

	results, err := Run([]Definition{{
		Name:     "handle probe",
		Argv:     windowsHelperArgv("signal-event", strconv.FormatUint(uint64(sentinel), 10)),
		Required: true,
	}}, repository, evidence)
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

func windowsHelperArgv(mode string, arguments ...string) []string {
	argv := []string{os.Args[0], "-test.run=^TestWindowsHandleProbeHelperProcess$", "--", mode}
	return append(argv, arguments...)
}
