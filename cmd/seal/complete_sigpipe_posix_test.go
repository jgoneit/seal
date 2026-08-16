//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const completeBrokenPipeHelper = "SEAL_COMPLETE_BROKEN_PIPE_HELPER"

func TestMainCompleteBrokenPipeReturnsRuntimeExit(t *testing.T) {
	if os.Getenv(completeBrokenPipeHelper) == "1" {
		configureProcessSignals()
		os.Exit(completeTask(
			os.Getenv("SEAL_COMPLETE_REPOSITORY"),
			os.Getenv("SEAL_COMPLETE_TASK_ID"),
			os.Getenv("SEAL_COMPLETE_RUN_ID"),
			os.Stdout,
			os.Stderr,
		))
	}

	fixture := completeTestFixture(t)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}
	runID := completeTestVerify(t, fixture.repository)

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := readPipe.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMainCompleteBrokenPipeReturnsRuntimeExit$")
	command.Env = append(os.Environ(),
		completeBrokenPipeHelper+"=1",
		"SEAL_COMPLETE_REPOSITORY="+fixture.repository,
		"SEAL_COMPLETE_TASK_ID="+createContractTaskID,
		"SEAL_COMPLETE_RUN_ID="+runID,
	)
	command.Stdout = writePipe
	var stderr bytes.Buffer
	command.Stderr = &stderr
	runErr := command.Run()
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	var exitError *exec.ExitError
	if !errors.As(runErr, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("broken-pipe helper error = %v, stderr = %q", runErr, stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not write Completion output") {
		t.Fatalf("broken-pipe stderr = %q", stderr.String())
	}
	completionPath := filepath.Join(
		fixture.repository,
		".seal", "evidence", createContractTaskID, runID, "completion.json",
	)
	if info, err := os.Lstat(completionPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("completion record after broken pipe = %v, %v", info, err)
	}
}
