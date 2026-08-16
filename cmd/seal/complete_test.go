package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCLICompletePublishesAndReusesImmutableV2(t *testing.T) {
	fixture := completeTestFixture(t)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}
	runID := completeTestVerify(t, fixture.repository)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(
		fixture.repository,
		[]string{"complete", createContractTaskID, "--run-id", runID},
		&stdout,
		&stderr,
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("complete code = %d, stderr = %q", code, stderr.String())
	}
	result := decodeSingleJSON(t, stdout.Bytes()).(map[string]any)
	resolvedRepository, err := filepath.EvalSymlinks(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	completionPath := filepath.Join(
		resolvedRepository,
		".seal", "evidence", createContractTaskID, runID, "completion.json",
	)
	if result["completion_path"] != completionPath ||
		result["run_id"] != runID || result["task_id"] != createContractTaskID {
		t.Fatalf("completion result = %#v", result)
	}
	contents, err := os.ReadFile(completionPath)
	if err != nil {
		t.Fatal(err)
	}
	record := decodeSingleJSON(t, contents).(map[string]any)
	if len(record) != 8 || record["schema_version"] != float64(2) ||
		record["task_id"] != createContractTaskID || record["run_id"] != runID ||
		record["final_result"] != "pass" {
		t.Fatalf("completion record = %#v", record)
	}
	verified, _ := record["verified_source_sha256"].(string)
	current, _ := record["current_source_sha256"].(string)
	evidence, _ := record["evidence_sha256"].(string)
	completedAt, _ := record["completed_at"].(string)
	if len(evidence) != 64 || len(verified) != 64 || current != verified {
		t.Fatalf("completion digests = evidence %q, verified %q, current %q", evidence, verified, current)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", completedAt); err != nil {
		t.Fatalf("completed_at = %q: %v", completedAt, err)
	}

	preservedTime := time.Unix(1_234_567_890, 0)
	if err := os.Chtimes(completionPath, preservedTime, preservedTime); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	code = runCLI(
		fixture.repository,
		[]string{"complete", "--run-id=" + runID, createContractTaskID},
		&stdout,
		&stderr,
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("idempotent complete code = %d, stderr = %q", code, stderr.String())
	}
	after, err := os.ReadFile(completionPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(completionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, contents) || !info.ModTime().Equal(preservedTime) {
		t.Fatalf("idempotent Complete rewrote the record: mtime = %v", info.ModTime())
	}
}

func TestRunCLICompleteStdoutFailurePreservesCommittedRecord(t *testing.T) {
	fixture := completeTestFixture(t)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}
	runID := completeTestVerify(t, fixture.repository)

	var stderr bytes.Buffer
	code := completeTask(
		fixture.repository,
		createContractTaskID,
		runID,
		failingOutput{},
		&stderr,
	)
	if code != 1 || !strings.Contains(stderr.String(), "could not write Completion output") {
		t.Fatalf("complete code = %d, stderr = %q", code, stderr.String())
	}
	completionPath := filepath.Join(
		fixture.repository,
		".seal", "evidence", createContractTaskID, runID, "completion.json",
	)
	if info, err := os.Lstat(completionPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("completion record after stdout failure = %v, %v", info, err)
	}
}

func TestParseCompleteRequiresExplicitIdentities(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantTask  string
		wantRun   string
		wantError bool
	}{
		{name: "documented order", args: []string{"TASK-1", "--run-id", "RUN-1"}, wantTask: "TASK-1", wantRun: "RUN-1"},
		{name: "option first", args: []string{"--run-id", "RUN-1", "TASK-1"}, wantTask: "TASK-1", wantRun: "RUN-1"},
		{name: "equals", args: []string{"TASK-1", "--run-id=RUN-1"}, wantTask: "TASK-1", wantRun: "RUN-1"},
		{name: "last repeated value wins", args: []string{"TASK-1", "--run-id", "RUN-1", "--run-id=RUN-2"}, wantTask: "TASK-1", wantRun: "RUN-2"},
		{name: "empty equals reaches identity validation", args: []string{"TASK-1", "--run-id="}, wantTask: "TASK-1", wantRun: ""},
		{name: "terminator", args: []string{"--run-id", "RUN-1", "--", "TASK-1"}, wantTask: "TASK-1", wantRun: "RUN-1"},
		{name: "trailing terminator", args: []string{"TASK-1", "--run-id", "RUN-1", "--"}, wantTask: "TASK-1", wantRun: "RUN-1"},
		{name: "missing task", args: []string{"--run-id", "RUN-1"}, wantError: true},
		{name: "missing run option", args: []string{"TASK-1"}, wantError: true},
		{name: "missing separated value", args: []string{"TASK-1", "--run-id"}, wantError: true},
		{name: "option cannot be separated value", args: []string{"TASK-1", "--run-id", "--help"}, wantError: true},
		{name: "extra positional", args: []string{"TASK-1", "TASK-2", "--run-id", "RUN-1"}, wantError: true},
		{name: "unsupported option", args: []string{"TASK-1", "--latest", "--run-id", "RUN-1"}, wantError: true},
		{name: "argparse prefix alias is not public", args: []string{"TASK-1", "--r", "RUN-1"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			taskID, runID, err := parseComplete(test.args)
			if test.wantError {
				if err == nil {
					t.Fatalf("parseComplete(%q) = %q, %q, nil; want error", test.args, taskID, runID)
				}
				return
			}
			if err != nil || taskID != test.wantTask || runID != test.wantRun {
				t.Fatalf("parseComplete(%q) = %q, %q, %v", test.args, taskID, runID, err)
			}
		})
	}
}

func TestRunCLICompleteHelpHonorsOptionTerminator(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantHelp   bool
		wantStderr string
	}{
		{name: "long help", args: []string{"complete", "--help"}, wantHelp: true},
		{name: "short help after identity", args: []string{"complete", "TASK-1", "-h"}, wantHelp: true},
		{name: "help after valid option", args: []string{"complete", "TASK-1", "--run-id", "RUN-1", "--help"}, wantHelp: true},
		{name: "help is not a separated value", args: []string{"complete", "TASK-1", "--run-id", "--help"}, wantStderr: "complete requires --run-id"},
		{name: "help after terminator is positional", args: []string{"complete", "--run-id", "RUN-1", "--", "--help"}, wantStderr: "Task id must begin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runCLI(t.TempDir(), test.args, &stdout, &stderr)
			if test.wantHelp {
				if code != 0 || stdout.String() != completeHelp || stderr.Len() != 0 {
					t.Fatalf("help result = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
				}
				return
			}
			if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("non-help result = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func completeTestVerify(t *testing.T, repository string) string {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runCLI(repository, []string{"verify", createContractTaskID}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify code = %d, stderr = %q", code, stderr.String())
	}
	result := decodeSingleJSON(t, stdout.Bytes()).(map[string]any)
	runID, ok := result["run_id"].(string)
	if !ok || len(runID) != 32 {
		t.Fatalf("verify run_id = %#v", result["run_id"])
	}
	return runID
}

func completeTestFixture(t *testing.T) createContractFixture {
	t.Helper()
	fixture := createContractNewFixture(t, true)
	specification := createContractValidSpec()
	specification["scope"] = []any{"."}
	createContractWriteJSON(t, fixture.input, specification)
	catalog := createContractValidCatalog()
	catalog["checks"] = []any{map[string]any{
		"name":     "unit",
		"argv":     []any{"git", "rev-parse", "--verify", "HEAD"},
		"required": true,
	}}
	createContractWriteJSON(t, createContractCatalogPath(fixture.repository), catalog)
	return fixture
}
