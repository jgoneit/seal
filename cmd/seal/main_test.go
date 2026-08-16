package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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
		{name: "task create long help", args: []string{"task", "create", "--help"}, wantOutput: taskCreateHelp},
		{name: "task create short help", args: []string{"task", "create", "-h"}, wantOutput: taskCreateHelp},
		{name: "task create help after options", args: []string{"task", "create", "--force", "--file=input.json", "--help"}, wantOutput: taskCreateHelp},
		{name: "verify long help", args: []string{"verify", "--help"}, wantOutput: verifyHelp},
		{name: "verify short help", args: []string{"verify", "TASK-001", "-h"}, wantOutput: verifyHelp},
		{name: "complete long help", args: []string{"complete", "--help"}, wantOutput: completeHelp},
		{name: "complete short help", args: []string{"complete", "TASK-001", "--run-id", "RUN-001", "-h"}, wantOutput: completeHelp},
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

func TestMainInformationalCommandsIgnoreDeletedWorkingDirectory(t *testing.T) {
	// Frozen Reference 94bb931 handles exact informational flags before any
	// repository or working-directory lookup; normal and deleted cwd output is
	// byte-identical.
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit removing a process's current directory")
	}
	tests := []struct {
		name       string
		args       []string
		wantOutput string
	}{
		{name: "help", args: []string{"--help"}, wantOutput: help},
		{name: "version", args: []string{"--version"}, wantOutput: version + "\n"},
		{name: "task create long help", args: []string{"task", "create", "--help"}, wantOutput: taskCreateHelp},
		{name: "task create short help", args: []string{"task", "create", "-h"}, wantOutput: taskCreateHelp},
		{name: "verify help", args: []string{"verify", "--help"}, wantOutput: verifyHelp},
		{name: "complete help", args: []string{"complete", "--help"}, wantOutput: completeHelp},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normal := runMainSubprocess(t, false, test.args...)
			deleted := runMainSubprocess(t, true, test.args...)
			for mode, result := range map[string]mainSubprocessResult{"normal": normal, "deleted": deleted} {
				if result.code != 0 {
					t.Fatalf("%s cwd exit code = %d, stderr = %q", mode, result.code, result.stderr)
				}
				if result.stdout != test.wantOutput {
					t.Fatalf("%s cwd stdout = %q, want %q", mode, result.stdout, test.wantOutput)
				}
				if result.stderr != "" {
					t.Fatalf("%s cwd stderr = %q, want empty", mode, result.stderr)
				}
			}
			if normal != deleted {
				t.Fatalf("normal cwd result = %#v, deleted cwd result = %#v", normal, deleted)
			}
		})
	}
}

func TestMainStateCommandsApplyApprovedDeletedCWDRepositoryFailureWithoutWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit removing a process's current directory")
	}
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "valid task show",
			args:       []string{"task", "show", "TASK-001"},
			wantCode:   3,
			wantStderr: "error: Task commands must run inside a Git repository.\n",
		},
		{
			name:       "valid run show",
			args:       []string{"run", "show", "TASK-001", "--run-id", "RUN-001"},
			wantCode:   3,
			wantStderr: "error: Task commands must run inside a Git repository.\n",
		},
		{
			name:       "valid verify",
			args:       []string{"verify", "TASK-001"},
			wantCode:   3,
			wantStderr: "error: Task commands must run inside a Git repository.\n",
		},
		{
			name:       "valid complete",
			args:       []string{"complete", "TASK-001", "--run-id", "RUN-001"},
			wantCode:   3,
			wantStderr: "error: Task commands must run inside a Git repository.\n",
		},
		{
			name:       "invalid task id remains invalid input",
			args:       []string{"task", "show", "../TASK-001"},
			wantCode:   2,
			wantStderr: "error: Task id must begin with an alphanumeric character and contain only letters, numbers, underscores, or hyphens.\n",
		},
		{
			name:       "invalid run id remains invalid input",
			args:       []string{"run", "show", "TASK-001", "--run-id", "../RUN-001"},
			wantCode:   2,
			wantStderr: "error: Run id must begin with an alphanumeric character and contain only letters, numbers, underscores, or hyphens.\n",
		},
		{
			name:     "missing run id remains usage error",
			args:     []string{"run", "show", "TASK-001"},
			wantCode: 2,
			wantStderr: "error: run show requires --run-id <RUN_ID>\n" +
				"usage: seal task create --file <TASK_JSON> [--force]\n" +
				"       seal task show <TASK_ID>\n" +
				"       seal verify <TASK_ID>\n" +
				"       seal run show <TASK_ID> --run-id <RUN_ID>\n" +
				"       seal complete <TASK_ID> --run-id <RUN_ID>\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			workingDirectory := filepath.Join(root, "cwd")
			if err := os.Mkdir(workingDirectory, 0o755); err != nil {
				t.Fatalf("Mkdir(%q): %v", workingDirectory, err)
			}
			mustWriteTestFile(t, filepath.Join(root, "outside", ".seal", "sentinel"), []byte("unchanged\n"))
			wantTree := snapshotTestTree(t, root)
			delete(wantTree, "cwd") // The subprocess helper intentionally unlinks its cwd.

			result := runMainSubprocessAt(t, workingDirectory, true, test.args...)
			want := mainSubprocessResult{
				code:   test.wantCode,
				stderr: test.wantStderr,
			}
			if result != want {
				t.Fatalf("result = %#v, want %#v", result, want)
			}
			if after := snapshotTestTree(t, root); !reflect.DeepEqual(after, wantTree) {
				t.Fatalf("deleted-cwd command changed the surrounding tree:\nbefore: %#v\nafter:  %#v", wantTree, after)
			}
		})
	}
}

type mainSubprocessResult struct {
	code   int
	stdout string
	stderr string
}

func runMainSubprocess(t *testing.T, deleteWorkingDirectory bool, args ...string) mainSubprocessResult {
	t.Helper()
	workingDirectory := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(workingDirectory, 0o755); err != nil {
		t.Fatalf("Mkdir(%q): %v", workingDirectory, err)
	}
	return runMainSubprocessAt(t, workingDirectory, deleteWorkingDirectory, args...)
}

func runMainSubprocessAt(t *testing.T, workingDirectory string, deleteWorkingDirectory bool, args ...string) mainSubprocessResult {
	t.Helper()
	commandArgs := append([]string{"-test.run=^TestMainSubprocessHelper$", "--"}, args...)
	command := exec.Command(os.Args[0], commandArgs...)
	command.Dir = workingDirectory
	command.Env = append(
		os.Environ(),
		"SEAL_MAIN_SUBPROCESS_HELPER=1",
		"SEAL_MAIN_DELETE_CWD="+strconv.FormatBool(deleteWorkingDirectory),
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("run helper process: %v", err)
		}
		code = exitError.ExitCode()
	}
	return mainSubprocessResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestMainSubprocessHelper(t *testing.T) {
	if os.Getenv("SEAL_MAIN_SUBPROCESS_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "helper: missing argument separator")
		os.Exit(97)
	}
	if os.Getenv("SEAL_MAIN_DELETE_CWD") == "true" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "helper: resolve working directory: %v\n", err)
			os.Exit(97)
		}
		if err := os.Remove(workingDirectory); err != nil {
			fmt.Fprintf(os.Stderr, "helper: remove working directory: %v\n", err)
			os.Exit(97)
		}
		if err := os.Unsetenv("PWD"); err != nil {
			fmt.Fprintf(os.Stderr, "helper: unset PWD: %v\n", err)
			os.Exit(97)
		}
	}
	os.Args = append([]string{"seal"}, os.Args[separator+1:]...)
	main()
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

func TestRunCLITaskShowAppliesApprovedJSONDepthLimitWithoutWrites(t *testing.T) {
	// The frozen Reference renders roughly 200 MB for this 10,000-depth value.
	// This approved-divergence regression creates only the compact input in a
	// temporary repository and never materializes or stores Reference stdout.
	repository := initTestRepository(t)
	taskPath := filepath.Join(repository, ".seal", "tasks", "TASK-DEPTH-LIMIT.json")
	contents := `{"value":` + strings.Repeat("[", 10_000) + `0` + strings.Repeat("]", 10_000) + `}`
	mustWriteTestFile(t, taskPath, []byte(contents))
	before := snapshotTestTree(t, repository)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(repository, []string{"task", "show", "TASK-DEPTH-LIMIT"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runCLI() code = %d, want 1; stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout length = %d, want zero", stdout.Len())
	}
	wantStderr := "error: Task snapshot 'TASK-DEPTH-LIMIT' exceeds the supported JSON nesting depth.\n"
	if got := stderr.String(); got != wantStderr {
		t.Fatalf("stderr = %q, want %q", got, wantStderr)
	}
	if after := snapshotTestTree(t, repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("task show changed the repository\nbefore: %#v\nafter: %#v", before, after)
	}
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
