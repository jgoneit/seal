package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCLIVerifyPublishesRunReadableByRunShow(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(fixture.repository, []string{"verify", createContractTaskID}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("verify code = %d, stderr = %q", code, stderr.String())
	}
	result := decodeSingleJSON(t, stdout.Bytes()).(map[string]any)
	runID, ok := result["run_id"].(string)
	if !ok || len(runID) != 32 {
		t.Fatalf("run_id = %#v", result["run_id"])
	}
	resolvedRepository, err := filepath.EvalSymlinks(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(resolvedRepository, ".seal", "evidence", createContractTaskID, runID)
	if result["evidence_path"] != wantPath {
		t.Fatalf("evidence_path = %#v, want %q", result["evidence_path"], wantPath)
	}

	stdout.Reset()
	code = runCLI(
		fixture.repository,
		[]string{"run", "show", createContractTaskID, "--run-id", runID},
		&stdout,
		&stderr,
	)
	if code != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("run show code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunCLIVerifyMapsInputAndRepositoryFailures(t *testing.T) {
	tests := []struct {
		name     string
		cwd      string
		args     []string
		wantCode int
		contains string
	}{
		{
			name:     "invalid identity",
			cwd:      t.TempDir(),
			args:     []string{"verify", "../TASK"},
			wantCode: 2,
			contains: "Task id must begin",
		},
		{
			name:     "missing repository",
			cwd:      t.TempDir(),
			args:     []string{"verify", "TASK-001"},
			wantCode: 3,
			contains: "Task commands must run inside a Git repository",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runCLI(test.cwd, test.args, &stdout, &stderr); code != test.wantCode {
				t.Fatalf("code = %d, want %d; stderr = %q", code, test.wantCode, stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.contains) {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestVerifyStdoutFailureDoesNotRollBackCommittedRun(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}
	var stderr bytes.Buffer
	code := verifyTask(fixture.repository, createContractTaskID, failingOutput{}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "could not write verification output") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	parent := filepath.Join(fixture.repository, ".seal", "evidence", createContractTaskID)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() || strings.HasPrefix(entries[0].Name(), ".tmp-") {
		t.Fatalf("committed Runs = %v, want one published Run", entries)
	}
}

func TestParseVerifyRequiresOnePositionalIdentity(t *testing.T) {
	tests := []struct {
		args      []string
		want      string
		wantError bool
	}{
		{args: []string{"TASK-1"}, want: "TASK-1"},
		{args: []string{"--", "TASK-1"}, want: "TASK-1"},
		{args: []string{"TASK-1", "--"}, want: "TASK-1"},
		{args: []string{"--", "-TASK"}, want: "-TASK"},
		{args: nil, wantError: true},
		{args: []string{"TASK-1", "TASK-2"}, wantError: true},
		{args: []string{"--base-ref", "main"}, wantError: true},
		{args: []string{"--run-id=RUN"}, wantError: true},
	}
	for _, test := range tests {
		got, err := parseVerify(test.args)
		if test.wantError {
			if err == nil {
				t.Fatalf("parseVerify(%q) = %q, nil; want error", test.args, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("parseVerify(%q) = %q, %v; want %q, nil", test.args, got, err, test.want)
		}
	}
}

func TestRunCLIVerifyTreatsHelpAfterTerminatorAsPositional(t *testing.T) {
	tests := [][]string{
		{"verify", "--", "--help"},
		{"verify", "--", "-h"},
		{"verify", "TASK-1", "--", "--help"},
		{"verify", "TASK-1", "--", "-h"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runCLI(t.TempDir(), args, &stdout, &stderr); code != 2 {
				t.Fatalf("runCLI(%q) code = %d, stderr = %q; want 2", args, code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("runCLI(%q) stdout = %q; want empty", args, stdout.String())
			}
			if stderr.Len() == 0 {
				t.Fatalf("runCLI(%q) stderr = %q; want positional error", args, stderr.String())
			}
		})
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) {
	return 0, errors.New("injected stdout failure")
}

var _ io.Writer = failingOutput{}
