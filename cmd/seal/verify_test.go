package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jgoneit/seal/internal/runstate"
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

func TestRunCLIVerifyRejectsSavedTimeoutAboveBasicProfileMaximum(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	catalog := createContractValidCatalog()
	check := catalog["checks"].([]any)[0].(map[string]any)
	check["timeout_seconds"] = json.Number("301")
	createContractWriteJSON(t, createContractCatalogPath(fixture.repository), catalog)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(fixture.repository, []string{"verify", createContractTaskID}, &stdout, &stderr)
	want := "error: saved Task snapshot checks[0].timeout_seconds must be at most 300 seconds.\n"
	if code != 2 || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("verify = code %d, stdout %q, stderr %q; want exit 2 and %q", code, stdout.String(), stderr.String(), want)
	}
	assertVerifyHasNoRunResidue(t, fixture.repository)
}

func TestRunCLIVerifyMapsStreamLimitToExitThreeWithoutOutputOrRun(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	catalog := createContractValidCatalog()
	check := catalog["checks"].([]any)[0].(map[string]any)
	check["argv"] = []any{
		os.Args[0], "-test.run=^TestRunCLIVerifyOutputHelper$", "--", strconv.Itoa(8*1024*1024 + 1),
	}
	check["timeout_seconds"] = json.Number("30")
	createContractWriteJSON(t, createContractCatalogPath(fixture.repository), catalog)
	created := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	if created.code != 0 {
		t.Fatalf("task create failed: %s", created.stderr)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(fixture.repository, []string{"verify", createContractTaskID}, &stdout, &stderr)
	want := "error: checks[0].stdout exceeded the 8388608-byte safety limit.\n"
	if code != 3 || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("verify = code %d, stdout %q, stderr %q; want exit 3 and %q", code, stdout.String(), stderr.String(), want)
	}
	assertVerifyHasNoRunResidue(t, fixture.repository)
}

func TestRunCLIVerifyOutputHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	remaining, err := strconv.Atoi(os.Args[separator+1])
	if err != nil || remaining < 0 {
		os.Exit(96)
	}
	chunk := make([]byte, 64*1024)
	for remaining > 0 {
		length := len(chunk)
		if remaining < length {
			length = remaining
		}
		written, err := os.Stdout.Write(chunk[:length])
		if err != nil {
			os.Exit(96)
		}
		remaining -= written
	}
	os.Exit(0)
}

func assertVerifyHasNoRunResidue(t *testing.T, repository string) {
	t.Helper()
	parent := filepath.Join(repository, ".seal", "evidence", createContractTaskID)
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		t.Fatalf("unexpected verification residue: %v", names)
	}
}

func TestVerifyRejectsUnsafeEvidenceWriterAncestorMatrix(t *testing.T) {
	type boundary struct {
		name     string
		relative func(string) string
		gate     bool
	}
	type unsafeKind struct {
		name    string
		symlink bool
	}
	boundaries := []boundary{
		{name: "seal", relative: func(string) string { return ".seal" }, gate: true},
		{name: "evidence", relative: func(string) string { return filepath.Join(".seal", "evidence") }},
		{name: "task", relative: func(taskID string) string {
			return filepath.Join(".seal", "evidence", taskID)
		}},
	}
	kinds := []unsafeKind{
		{name: "symlink", symlink: true},
		{name: "broken_symlink", symlink: true},
		{name: "non_directory"},
	}
	surfaces := []struct {
		name   string
		invoke func(string) verifyAncestorInvocation
		assert func(*testing.T, verifyAncestorInvocation)
	}{
		{
			name: "api",
			invoke: func(repository string) verifyAncestorInvocation {
				_, err := runstate.Verify(repository, createContractTaskID)
				return verifyAncestorInvocation{err: err}
			},
			assert: func(t *testing.T, result verifyAncestorInvocation) {
				t.Helper()
				var repositoryError *runstate.RepositoryError
				if !errors.As(result.err, &repositoryError) {
					t.Fatalf("Verify error = %T %v, want RepositoryError", result.err, result.err)
				}
				if got := runstate.KindOf(result.err); got != runstate.KindRepository {
					t.Fatalf("Verify error kind = %v, want %v", got, runstate.KindRepository)
				}
			},
		},
		{
			name: "cli",
			invoke: func(repository string) verifyAncestorInvocation {
				var stdout bytes.Buffer
				var stderr bytes.Buffer
				code := runCLI(
					repository,
					[]string{"verify", createContractTaskID},
					&stdout,
					&stderr,
				)
				return verifyAncestorInvocation{
					code:   code,
					stdout: stdout.String(),
					stderr: stderr.String(),
				}
			},
			assert: func(t *testing.T, result verifyAncestorInvocation) {
				t.Helper()
				if result.code != 3 || result.stdout != "" || result.stderr == "" {
					t.Fatalf(
						"verify CLI = code %d, stdout %q, stderr %q; want RepositoryError exit 3",
						result.code,
						result.stdout,
						result.stderr,
					)
				}
			},
		},
	}

	for _, boundary := range boundaries {
		for _, kind := range kinds {
			leafName := boundary.name + "/" + kind.name
			t.Run(leafName, func(t *testing.T) {
				if kind.symlink {
					verifyAncestorRequireSymlink(t)
				}
				for _, surface := range surfaces {
					t.Run(surface.name, func(t *testing.T) {
						fixture := createContractNewFixture(t, true)
						created := createContractRun(
							t,
							fixture.repository,
							"task", "create", "--file", fixture.input,
						)
						if created.code != 0 {
							t.Fatalf("task create failed: %s", created.stderr)
						}
						repository, err := filepath.EvalSymlinks(fixture.repository)
						if err != nil {
							t.Fatal(err)
						}
						outside := filepath.Join(t.TempDir(), "outside")
						if err := os.MkdirAll(outside, 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(
							filepath.Join(outside, "sentinel"),
							[]byte("outside sentinel\n"),
							0o600,
						); err != nil {
							t.Fatal(err)
						}
						ancestor := filepath.Join(
							repository,
							boundary.relative(createContractTaskID),
						)

						result, backup := verifyAncestorInvoke(
							t,
							repository,
							ancestor,
							outside,
							kind.name,
							boundary.gate,
							func() verifyAncestorInvocation { return surface.invoke(repository) },
						)
						surface.assert(t, result)
						verifyAncestorAssertNoRunResidue(t, repository, outside, backup)
					})
				}
			})
		}
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

type verifyAncestorInvocation struct {
	err    error
	code   int
	stdout string
	stderr string
}

type verifyAncestorSnapshotEntry struct {
	mode   fs.FileMode
	data   string
	target string
}

func verifyAncestorRequireSymlink(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	link := filepath.Join(directory, "link")
	if err := os.Symlink(filepath.Join(directory, "missing"), link); err != nil {
		t.Skipf("symlink fixture unavailable on this platform: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
}

func verifyAncestorInvoke(
	t *testing.T,
	repository string,
	ancestor string,
	outside string,
	kind string,
	gate bool,
	invoke func() verifyAncestorInvocation,
) (verifyAncestorInvocation, string) {
	t.Helper()
	if !gate {
		if err := os.MkdirAll(filepath.Dir(ancestor), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := verifyAncestorMakeUnsafe(ancestor, outside, kind); err != nil {
			t.Fatal(err)
		}
		before, err := verifyAncestorSnapshot(outside)
		if err != nil {
			t.Fatal(err)
		}
		result := invoke()
		verifyAncestorAssertSnapshot(t, outside, before)
		return result, ""
	}

	gateDirectory, ready, release := verifyAncestorInstallGitGate(t)
	released := false
	releaseGate := func() {
		if released {
			return
		}
		released = true
		_ = os.WriteFile(release, []byte("release\n"), 0o600)
	}
	t.Cleanup(releaseGate)

	resultChannel := make(chan verifyAncestorInvocation, 1)
	go func() {
		resultChannel <- invoke()
	}()
	verifyAncestorWaitForGate(t, ready, resultChannel)

	backup := filepath.Join(gateDirectory, "original-seal")
	if err := os.Rename(ancestor, backup); err != nil {
		releaseGate()
		t.Fatal(err)
	}
	if err := verifyAncestorMakeUnsafe(ancestor, outside, kind); err != nil {
		releaseGate()
		t.Fatal(err)
	}
	before, err := verifyAncestorSnapshot(outside)
	if err != nil {
		releaseGate()
		t.Fatal(err)
	}
	releaseGate()

	var result verifyAncestorInvocation
	select {
	case result = <-resultChannel:
	case <-time.After(30 * time.Second):
		t.Fatal("Verify did not finish after releasing the Git gate")
	}
	verifyAncestorAssertSnapshot(t, outside, before)
	return result, backup
}

func verifyAncestorMakeUnsafe(ancestor, outside, kind string) error {
	switch kind {
	case "symlink":
		target := filepath.Join(outside, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			return err
		}
		return os.Symlink(target, ancestor)
	case "broken_symlink":
		return os.Symlink(filepath.Join(outside, "missing"), ancestor)
	case "non_directory":
		return os.WriteFile(ancestor, []byte("not a directory\n"), 0o600)
	default:
		return fmt.Errorf("unknown unsafe ancestor kind %q", kind)
	}
}

func verifyAncestorInstallGitGate(t *testing.T) (directory, ready, release string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory = t.TempDir()
	ready = filepath.Join(directory, "ready")
	release = filepath.Join(directory, "release")
	once := filepath.Join(directory, "once")
	wrapperName := "git"
	wrapper := `#!/bin/sh
if mkdir "$SEAL_VERIFY_GATE_ONCE" 2>/dev/null; then
  : > "$SEAL_VERIFY_GATE_READY"
  while [ ! -e "$SEAL_VERIFY_GATE_RELEASE" ]; do
    sleep 0.01
  done
fi
exec "$SEAL_VERIFY_REAL_GIT" "$@"
`
	if runtime.GOOS == "windows" {
		wrapperName = "git.cmd"
		wrapper = `@echo off
2>nul mkdir "%SEAL_VERIFY_GATE_ONCE%"
if not errorlevel 1 (
  type nul > "%SEAL_VERIFY_GATE_READY%"
  :seal_verify_wait
  if not exist "%SEAL_VERIFY_GATE_RELEASE%" (
    >nul ping 127.0.0.1 -n 2
    goto seal_verify_wait
  )
)
"%SEAL_VERIFY_REAL_GIT%" %*
exit /b %errorlevel%
`
	}
	if err := os.WriteFile(filepath.Join(directory, wrapperName), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEAL_VERIFY_GATE_ONCE", once)
	t.Setenv("SEAL_VERIFY_GATE_READY", ready)
	t.Setenv("SEAL_VERIFY_GATE_RELEASE", release)
	t.Setenv("SEAL_VERIFY_REAL_GIT", realGit)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return directory, ready, release
}

func verifyAncestorWaitForGate(
	t *testing.T,
	ready string,
	result <-chan verifyAncestorInvocation,
) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case early := <-result:
			t.Fatalf("Verify returned before the Git gate: %+v", early)
		case <-deadline.C:
			t.Fatal("Verify did not reach the Git gate")
		case <-ticker.C:
		}
	}
}

func verifyAncestorSnapshot(root string) (map[string]verifyAncestorSnapshotEntry, error) {
	snapshot := make(map[string]verifyAncestorSnapshotEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := verifyAncestorSnapshotEntry{mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.target, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var contents []byte
			contents, err = os.ReadFile(path)
			item.data = string(contents)
		}
		if err != nil {
			return err
		}
		snapshot[relative] = item
		return nil
	})
	return snapshot, err
}

func verifyAncestorAssertSnapshot(
	t *testing.T,
	root string,
	want map[string]verifyAncestorSnapshotEntry,
) {
	t.Helper()
	got, err := verifyAncestorSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outside tree changed: got %#v, want %#v", got, want)
	}
}

func verifyAncestorAssertNoRunResidue(t *testing.T, roots ...string) {
	t.Helper()
	for _, root := range roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".tmp-") || (entry.IsDir() && verifyAncestorIsRunID(name)) {
				return fmt.Errorf("unexpected Run or staging residue at %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func verifyAncestorIsRunID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
