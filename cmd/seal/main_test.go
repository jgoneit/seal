package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	fixtureTaskID = "SL-CONFIG-TEST-ISOLATION-001"
	fixtureRunID  = "58afd1bb1b4d4e8397aaabff53a6ae7a"
)

func TestRunCLIInformationalCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOutput string
	}{
		{name: "help", args: []string{"--help"}, wantOutput: help},
		{name: "version", args: []string{"--version"}, wantOutput: version + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runCLI(t.TempDir(), test.args, &stdout, &stderr); code != 0 {
				t.Fatalf("runCLI() code = %d, stderr = %q", code, stderr.String())
			}
			if got := stdout.String(); got != test.wantOutput {
				t.Fatalf("stdout = %q, want %q", got, test.wantOutput)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunCLITaskShowReturnsStoredObjectWithoutSchemaAuthority(t *testing.T) {
	repository := initTestRepository(t)
	taskPath := filepath.Join(repository, ".seal", "tasks", "TASK-SCHEMA-INVALID.json")
	mustWriteTestFile(t, taskPath, []byte(`{"schema_version":99,"id":"DIFFERENT","unexpected":true}`))
	before := snapshotTestTree(t, filepath.Join(repository, ".seal"))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(repository, []string{"task", "show", "TASK-SCHEMA-INVALID"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runCLI() code = %d, stderr = %q", code, stderr.String())
	}
	want := "{\n  \"id\": \"DIFFERENT\",\n  \"schema_version\": 99,\n  \"unexpected\": true\n}\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if after := snapshotTestTree(t, filepath.Join(repository, ".seal")); !reflect.DeepEqual(after, before) {
		t.Fatalf("task show changed .seal\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestRunCLITaskShowMapsHandledFailures(t *testing.T) {
	repository := initTestRepository(t)
	mustWriteTestFile(t, filepath.Join(repository, ".seal", "tasks", "MALFORMED.json"), []byte("{\n"))
	tests := []struct {
		name      string
		cwd       string
		args      []string
		wantCode  int
		wantError string
	}{
		{
			name:      "malformed stored JSON",
			cwd:       repository,
			args:      []string{"task", "show", "MALFORMED"},
			wantCode:  2,
			wantError: "error: Task snapshot 'MALFORMED' is not valid JSON:",
		},
		{
			name:      "missing repository",
			cwd:       t.TempDir(),
			args:      []string{"task", "show", "TASK-001"},
			wantCode:  3,
			wantError: "error: Task commands must run inside a Git repository.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runCLI(test.cwd, test.args, &stdout, &stderr); code != test.wantCode {
				t.Fatalf("runCLI() code = %d, want %d; stderr = %q", code, test.wantCode, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.HasPrefix(stderr.String(), test.wantError) {
				t.Fatalf("stderr = %q, want prefix %q", stderr.String(), test.wantError)
			}
		})
	}
}

func TestRunCLITaskShowPreservesReferenceEncodingOutcomes(t *testing.T) {
	repository := initTestRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustWriteTestFile(t, filepath.Join(tasks, "SURROGATE.json"), []byte(`{"value":"\udcff"}`))
	mustWriteTestFile(t, filepath.Join(tasks, "RAW-INVALID.json"), []byte{'{', '"', 'v', 'a', 'l', 'u', 'e', '"', ':', '"', 0xff, '"', '}'})
	mustWriteTestFile(t, filepath.Join(tasks, "INTEGER-LIMIT.json"), []byte(`{"value":`+strings.Repeat("7", 4301)+`}`))

	t.Run("surrogateescape is byte-oriented success", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runCLI(repository, []string{"task", "show", "SURROGATE"}, &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("runCLI() code = %d, stderr = %q", code, stderr.String())
		}
		want := append([]byte("{\n  \"value\": \""), 0xff)
		want = append(want, []byte("\"\n}\n")...)
		if !bytes.Equal(stdout.Bytes(), want) {
			t.Fatalf("stdout bytes = % x, want % x", stdout.Bytes(), want)
		}
	})

	t.Run("raw invalid UTF-8 keeps Reference exit one", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runCLI(repository, []string{"task", "show", "RAW-INVALID"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("runCLI() code = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = % x, want empty", stdout.Bytes())
		}
		if !strings.Contains(stderr.String(), "not valid UTF-8") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("oversized integer keeps Reference exit one", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runCLI(repository, []string{"task", "show", "INTEGER-LIMIT"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("runCLI() code = %d, want 1", code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout length = %d, want zero", stdout.Len())
		}
		if !strings.Contains(stderr.String(), "4300 digits") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})
}

func TestRunCLIShowsValidatedConformanceRunWithoutWrites(t *testing.T) {
	repository := conformanceRepository(t)
	before := snapshotTestTree(t, filepath.Join(repository, ".seal"))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(repository, []string{"run", "show", fixtureTaskID, "--run-id", fixtureRunID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runCLI() code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := decodeSingleJSON(t, stdout.Bytes())
	want := loadExpectedJSON(t, "run_regular_file")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("run summary mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	if after := snapshotTestTree(t, filepath.Join(repository, ".seal")); !reflect.DeepEqual(after, before) {
		t.Fatalf("run show changed .seal\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestRunCLIMapsCorruptEvidenceToExitEight(t *testing.T) {
	repository := conformanceRepository(t)
	diffPath := filepath.Join(repository, ".seal", "evidence", fixtureTaskID, fixtureRunID, "diff.patch")
	if err := os.Remove(diffPath); err != nil {
		t.Fatalf("Remove(%q): %v", diffPath, err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(repository, []string{"run", "show", fixtureTaskID, "--run-id", fixtureRunID}, &stdout, &stderr)
	if code != 8 {
		t.Fatalf("runCLI() code = %d, want 8; stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "error: Required evidence file is missing: diff.patch.") {
		t.Fatalf("stderr = %q, want Evidence error", stderr.String())
	}
}

func TestParseRunShowRequiresExplicitExactIdentities(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantTaskID string
		wantRunID  string
		wantError  bool
	}{
		{name: "documented order", args: []string{"TASK-1", "--run-id", "RUN-1"}, wantTaskID: "TASK-1", wantRunID: "RUN-1"},
		{name: "option first", args: []string{"--run-id", "RUN-1", "TASK-1"}, wantTaskID: "TASK-1", wantRunID: "RUN-1"},
		{name: "equals option", args: []string{"TASK-1", "--run-id=RUN-1"}, wantTaskID: "TASK-1", wantRunID: "RUN-1"},
		{name: "no latest inference", args: []string{"TASK-1"}, wantError: true},
		{name: "missing task", args: []string{"--run-id", "RUN-1"}, wantError: true},
		{name: "repeated option uses last value", args: []string{"TASK-1", "--run-id", "RUN-1", "--run-id=RUN-2"}, wantTaskID: "TASK-1", wantRunID: "RUN-2"},
		{name: "extra task", args: []string{"TASK-1", "TASK-2", "--run-id", "RUN-1"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			taskID, runID, err := parseRunShow(test.args)
			if test.wantError {
				if err == nil {
					t.Fatalf("parseRunShow() = (%q, %q, nil), want error", taskID, runID)
				}
				return
			}
			if err != nil || taskID != test.wantTaskID || runID != test.wantRunID {
				t.Fatalf("parseRunShow() = (%q, %q, %v), want (%q, %q, nil)", taskID, runID, err, test.wantTaskID, test.wantRunID)
			}
		})
	}
}

func initTestRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", repository, err)
	}
	command := exec.Command("git", "-C", repository, "init", "--quiet")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return repository
}

func conformanceRepository(t *testing.T) string {
	t.Helper()
	repository := initTestRepository(t)
	source := filepath.Join("..", "..", "conformance", "fixtures", "base-pass", ".seal")
	copyTestTree(t, source, filepath.Join(repository, ".seal"))
	return repository
}

func copyTestTree(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy tree %q to %q: %v", source, destination, err)
	}
}

func loadExpectedJSON(t *testing.T, caseID string) any {
	t.Helper()
	path := filepath.Join("..", "..", "conformance", "expected", "reference-results.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var envelope struct {
		Results map[string]struct {
			StdoutJSON any `json:"stdout_json"`
		} `json:"results"`
	}
	if err := json.Unmarshal(contents, &envelope); err != nil {
		t.Fatalf("Unmarshal(%q): %v", path, err)
	}
	result, ok := envelope.Results[caseID]
	if !ok {
		t.Fatalf("expected result %q is missing", caseID)
	}
	return result.StdoutJSON
}

func decodeSingleJSON(t *testing.T, contents []byte) any {
	t.Helper()
	if len(contents) == 0 || contents[len(contents)-1] != '\n' {
		t.Fatalf("stdout must end in one newline: %q", contents)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("Decode stdout: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("stdout contains trailing JSON: %v", err)
	}
	return value
}

func mustWriteTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func snapshotTestTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[filepath.ToSlash(relative)] = "directory"
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[filepath.ToSlash(relative)] = "symlink:" + target
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = fmt.Sprintf("file:%o:%x", info.Mode().Perm(), contents)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %q: %v", root, err)
	}
	return snapshot
}
