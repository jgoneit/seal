package runstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgoneit/seal/internal/taskstate"
)

func TestExportRunsEmptyRepositoryDoesNotCreateState(t *testing.T) {
	repository := t.TempDir()
	// A .git file is also a worktree repository marker; export needs no Git command.
	if err := os.WriteFile(filepath.Join(repository, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, repository)
	report, err := ExportRuns(repository, "test")
	if err != nil || !report.ScanComplete || len(report.Runs) != 0 || len(report.Issues) != 0 {
		t.Fatalf("empty export = %#v, %v", report, err)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, repository)) {
		t.Fatal("export created repository state")
	}
}

func TestExportRunsProjectsMetricsWithoutContentOrWrites(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	before := snapshotTree(t, fixture.repository)
	beforeTimes := exportFileTimes(t, fixture.repository)
	report, err := ExportRuns(fixture.repository, "0.3.0-test")
	if err != nil || !report.ScanComplete || len(report.Runs) != 1 {
		t.Fatalf("export = %#v, %v", report, err)
	}
	run := report.Runs[0]
	if run.TaskID != fixture.taskID || run.RunID != fixture.runID || run.Timestamp == nil ||
		run.MechanicalResult != "pass" || run.CompletionRecord.State != "absent" ||
		len(run.Checks) != 1 || run.Checks[0].DurationSeconds == nil || *run.Checks[0].DurationSeconds != 0.1 {
		t.Fatalf("run = %#v", run)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if report.ExporterVersion != "0.3.0-test" || run.RunVersion != nil || !bytes.Contains(encoded, []byte(`"run_version":null`)) || bytes.Contains(encoded, []byte(`"seal_version"`)) {
		t.Fatalf("historical producer version was invented: %s", encoded)
	}
	for _, forbidden := range []string{fixture.repository, "unit-test", "argv", "cwd", "stdout", "stderr", "diff.patch", "scope_violations", "go test"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("export contains private field/content %q", forbidden)
		}
	}
	if !reflect.DeepEqual(before, snapshotTree(t, fixture.repository)) ||
		!reflect.DeepEqual(beforeTimes, exportFileTimes(t, fixture.repository)) {
		t.Fatal("export changed repository bytes or modification times")
	}
}

func TestExportRunsPreservesRequiredCheckFailureAndIntegerExitCode(t *testing.T) {
	for _, exitCode := range []json.Number{"23", "9223372036854775808", "-9223372036854775809"} {
		t.Run(exitCode.String(), func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: false, exitCode: exitCode, sourceStable: true})
			if _, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID); err != nil {
				t.Fatalf("failure fixture must be valid saved Evidence: %v", err)
			}
			report, err := ExportRuns(fixture.repository, "test")
			if err != nil || !report.ScanComplete || len(report.Runs) != 1 || len(report.Issues) != 0 {
				t.Fatalf("failed check export = %#v, %v", report, err)
			}
			run := report.Runs[0]
			if run.MechanicalResult != "fail" || run.RequiredChecksPass || !run.ScopePass ||
				!run.SourceStableDuringChecks || run.ScopeViolationCount != 0 || len(run.Checks) != 1 ||
				run.CompletionRecord.State != "absent" || run.CompletionRecord.CompletedAt != nil {
				t.Fatalf("failed run projection = %#v", run)
			}
			check := run.Checks[0]
			if check.Index != 0 || !check.Required || check.Passed || check.TimedOut ||
				check.ExitCode == nil || *check.ExitCode != exitCode {
				t.Fatalf("failed check projection = %#v", check)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var exported struct {
				Runs []struct {
					Checks []struct {
						ExitCode json.RawMessage `json:"exit_code"`
					} `json:"checks"`
				} `json:"runs"`
			}
			if err := json.Unmarshal(encoded, &exported); err != nil {
				t.Fatal(err)
			}
			if got := string(exported.Runs[0].Checks[0].ExitCode); got != exitCode.String() {
				t.Fatalf("JSON exit_code = %s, want exact unquoted integer %s", got, exitCode)
			}
		})
	}
}

func TestExportRunsSortedExactIdentitiesAndStagingPublication(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: false, passed: false, exitCode: 7, sourceStable: true})
	copyExportRun(t, fixture, "AAA", "run-z")
	copyExportRun(t, fixture, fixture.taskID, "run-0000")
	staging := filepath.Join(filepath.Dir(fixture.runPath), ".tmp-run-new")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := ExportRuns(fixture.repository, "test")
	if err != nil || !report.ScanComplete || len(report.Runs) != 3 {
		t.Fatalf("export = %#v, %v", report, err)
	}
	identities := make([]string, len(report.Runs))
	for index, run := range report.Runs {
		identities[index] = run.TaskID + "/" + run.RunID
	}
	want := []string{"AAA/run-z", fixture.taskID + "/run-0000", fixture.taskID + "/" + fixture.runID}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("identities = %v, want %v", identities, want)
	}
	// A previously private staging directory becoming visible is found next scan.
	if err := os.Rename(staging, filepath.Join(filepath.Dir(staging), "run-new")); err != nil {
		t.Fatal(err)
	}
	report, err = ExportRuns(fixture.repository, "test")
	if err != nil || report.ScanComplete || len(report.Runs) != 3 || !hasExportIssue(report, "invalid_evidence") {
		t.Fatalf("published incomplete run must remain visible as an issue: %#v, %v", report, err)
	}
}

func TestExportRunsCorruptionIsPartialNotSilent(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	copyExportRun(t, fixture, fixture.taskID, "run-broken")
	if err := os.WriteFile(filepath.Join(filepath.Dir(fixture.runPath), "run-broken", "checks.json"), []byte("private broken data"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ExportRuns(fixture.repository, "test")
	if err != nil || report.ScanComplete || len(report.Runs) != 1 || !hasExportIssue(report, "invalid_evidence") {
		t.Fatalf("export = %#v, %v", report, err)
	}
	encoded, _ := json.Marshal(report)
	if bytes.Contains(encoded, []byte("private broken data")) || bytes.Contains(encoded, []byte(fixture.repository)) {
		t.Fatal("issue leaked content or path")
	}
}

func TestExportRunsRejectsLinksWithoutFollowingThem(t *testing.T) {
	for _, relative := range []string{
		".seal", ".seal/evidence", ".seal/tasks", ".seal/tasks/" + fixtureTaskID + ".json",
		".seal/evidence/" + fixtureTaskID, ".seal/evidence/" + fixtureTaskID + "/" + fixtureRunID,
	} {
		t.Run(relative, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			original := filepath.Join(fixture.repository, filepath.FromSlash(relative))
			moved := filepath.Join(t.TempDir(), "external-private-target")
			if err := os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, original); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			report, err := ExportRuns(fixture.repository, "test")
			if err != nil || report.ScanComplete || len(report.Runs) != 0 || !hasExportIssue(report, "unsafe_entry") {
				t.Fatalf("export = %#v, %v", report, err)
			}
		})
	}
}

func TestExportHistoricalCompletionWithoutCurrentSourceObservation(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	writeExportCompletion(t, fixture)
	if err := os.WriteFile(filepath.Join(fixture.repository, "later-change.txt"), []byte("changed after historical completion"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := exportFileTimes(t, fixture.repository)
	report, err := ExportRuns(fixture.repository, "test")
	if err != nil || !report.ScanComplete || report.Runs[0].CompletionRecord.State != "recorded_pass" || report.Runs[0].CompletionRecord.CompletedAt == nil {
		t.Fatalf("historical completion = %#v, %v", report, err)
	}
	if !reflect.DeepEqual(before, exportFileTimes(t, fixture.repository)) {
		t.Fatal("historical export changed state")
	}
}

func TestExportInvalidCompletionPreservesValidRun(t *testing.T) {
	for _, mode := range []string{"wrong-digest", "oversized", "symlink", "failed-run"} {
		t.Run(mode, func(t *testing.T) {
			options := fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true}
			if mode == "failed-run" {
				options.scopeViolation = true
			}
			fixture := newRunFixture(t, options)
			writeExportCompletion(t, fixture)
			path := filepath.Join(fixture.runPath, "completion.json")
			switch mode {
			case "wrong-digest":
				mutateTestJSON(t, path, func(document map[string]any) { document["evidence_sha256"] = strings.Repeat("f", 64) })
			case "oversized":
				if err := os.WriteFile(path, bytes.Repeat([]byte(" "), (64<<10)+1), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				moved := filepath.Join(t.TempDir(), "completion")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			report, err := ExportRuns(fixture.repository, "test")
			if err != nil || report.ScanComplete || len(report.Runs) != 1 ||
				report.Runs[0].CompletionRecord.State != "invalid" || !hasExportIssue(report, "invalid_completion") {
				t.Fatalf("invalid completion = %#v, %v", report, err)
			}
		})
	}
}

func TestExportLegacyNumericRequiredFlagsPreserveValidatedMeaning(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		required bool
	}{
		{"1", true}, {"1.0", true}, {"0", false}, {"0.0", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: tc.required, passed: false, exitCode: 7, sourceStable: true})
			replaceRawJSONField(t, filepath.Join(fixture.runPath, "checks.json"), "required", fmt.Sprint(tc.required), tc.raw)
			refreshExportManifest(t, fixture)
			if _, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID); err != nil {
				t.Fatalf("legacy required flag must remain valid saved Evidence: %v", err)
			}
			report, err := ExportRuns(fixture.repository, "test")
			if err != nil || !report.ScanComplete || len(report.Runs) != 1 || len(report.Issues) != 0 {
				t.Fatalf("legacy required export = %#v, %v", report, err)
			}
			run := report.Runs[0]
			wantResult := "pass"
			if tc.required {
				wantResult = "fail"
			}
			if len(run.Checks) != 1 || run.Checks[0].Required != tc.required || run.Checks[0].Passed ||
				run.RequiredChecksPass != !tc.required || run.MechanicalResult != wantResult {
				t.Fatalf("legacy required %s changed meaning: %#v", tc.raw, run)
			}
		})
	}
}

func TestExportLegacyNonFiniteMetricsBecomeNull(t *testing.T) {
	for _, rawDuration := range []string{"NaN", "Infinity", "1e400"} {
		t.Run(rawDuration, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			replaceRawJSONField(t, filepath.Join(fixture.runPath, "checks.json"), "duration_seconds", "0.1", rawDuration)
			mutateTestJSON(t, filepath.Join(fixture.runPath, "verification.json"), func(document map[string]any) { document["timestamp"] = "legacy-clock-value" })
			refreshExportManifest(t, fixture)
			if _, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID); err != nil {
				t.Fatalf("existing compatibility validator changed: %v", err)
			}
			report, err := ExportRuns(fixture.repository, "test")
			if err != nil || report.ScanComplete || len(report.Runs) != 1 || !hasExportIssue(report, "invalid_metric") ||
				report.Runs[0].Timestamp != nil || report.Runs[0].Checks[0].DurationSeconds != nil {
				t.Fatalf("metrics = %#v, %v", report, err)
			}
			if _, err := json.Marshal(report); err != nil {
				t.Fatalf("export must remain standard JSON: %v", err)
			}
		})
	}
}

func TestExportDirectoryLimitReportsIncomplete(t *testing.T) {
	for _, test := range []struct{ directory, entryFormat string }{
		{"evidence", "TASK-%05d"},
		{"tasks", ".task.tmp-%032x"},
	} {
		t.Run(test.directory, func(t *testing.T) {
			repository := t.TempDir()
			if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(repository, ".seal", test.directory)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			for index := 0; index <= exportEntryLimit; index++ {
				if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf(test.entryFormat, index)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := ExportRuns(repository, "test")
			if err != nil || report.ScanComplete || len(report.Tasks) != 0 || len(report.Runs) != 0 || !hasExportIssue(report, "scan_limit") {
				t.Fatalf("limit = %#v, %v", report, err)
			}
		})
	}
}

func TestExportTaskInventoryIgnoresPublishedTemporaryLink(t *testing.T) {
	repository := t.TempDir()
	verificationGit(t, repository, "init", "--quiet")
	verificationGit(t, repository, "-c", "user.name=Seal Export Test", "-c", "user.email=seal-export@example.invalid",
		"commit", "--quiet", "--allow-empty", "-m", "baseline")
	writeTestJSON(t, filepath.Join(repository, ".seal", "checks.json"), map[string]any{
		"schema_version": 1,
		"checks":         []any{map[string]any{"name": "unit", "argv": []any{"go", "test", "./..."}, "required": true}},
	})
	taskID := "TASK-PUBLISHED"
	taskFile := filepath.Join(repository, "task-spec.json")
	writeTestJSON(t, taskFile, map[string]any{
		"schema_version": 1, "id": taskID, "type": "test", "objective": "Export a published Task.",
		"scope": []any{"."}, "checks": []any{"unit"}, "risk": "low", "verifier": map[string]any{"required": false},
	})
	if _, err := taskstate.Create(repository, taskFile, false); err != nil {
		t.Fatal(err)
	}
	// Reproduce the no-force writer's successful hard-link publication when
	// both best-effort attempts to unlink its temporary name fail.
	tasks := filepath.Join(repository, ".seal", "tasks")
	if err := os.Link(filepath.Join(tasks, taskID+".json"), filepath.Join(tasks, ".task.tmp-"+strings.Repeat("0", 32))); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, repository)
	beforeTimes := exportFileTimes(t, repository)
	report, err := ExportRuns(repository, "test")
	if err != nil || !report.ScanComplete || len(report.Issues) != 0 || len(report.Tasks) != 1 ||
		report.Tasks[0].TaskID != taskID || len(report.Runs) != 0 {
		t.Fatalf("published Task with residual temporary link = %#v, %v", report, err)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, repository)) ||
		!reflect.DeepEqual(beforeTimes, exportFileTimes(t, repository)) {
		t.Fatal("export changed the published Task or temporary link")
	}
}

func TestExportTaskInventoryWithoutEvidence(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	if err := os.RemoveAll(filepath.Join(fixture.repository, ".seal", "evidence")); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, fixture.repository)
	report, err := ExportRuns(fixture.repository, "test")
	if err != nil || !report.ScanComplete || len(report.Tasks) != 1 || report.Tasks[0].TaskID != fixture.taskID || len(report.Runs) != 0 {
		t.Fatalf("task-only export = %#v, %v", report, err)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, fixture.repository)) {
		t.Fatal("task inventory changed repository state")
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, ".seal", "tasks", "BROKEN.json"), []byte(`{"private":"invalid task content"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = ExportRuns(fixture.repository, "test")
	if err != nil || report.ScanComplete || len(report.Tasks) != 1 || !hasExportIssue(report, "invalid_task") {
		t.Fatalf("corrupt task-only export = %#v, %v", report, err)
	}
	encoded, _ := json.Marshal(report)
	if bytes.Contains(encoded, []byte("invalid task content")) || bytes.Contains(encoded, []byte(fixture.repository)) {
		t.Fatal("task issue leaked content or path")
	}
}

func TestExportTaskInventoryIsSortedAndValidatesOrphans(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	task := readTestObject(t, fixture.taskPath)
	// Filename order puts a-.json before a.json; the public order is by Task ID.
	for _, taskID := range []string{"AAA", "a", "a-"} {
		task["id"] = taskID
		writeTestJSON(t, filepath.Join(filepath.Dir(fixture.taskPath), taskID+".json"), task)
	}
	report, err := ExportRuns(fixture.repository, "test")
	if err != nil || !report.ScanComplete || len(report.Runs) != 1 {
		t.Fatalf("sorted saved Task inventory = %#v, %v", report, err)
	}
	identities := make([]string, len(report.Tasks))
	for index, task := range report.Tasks {
		identities[index] = task.TaskID
	}
	if want := []string{"AAA", fixture.taskID, "a", "a-"}; !reflect.DeepEqual(identities, want) {
		t.Fatalf("Task inventory = %v, want %v", identities, want)
	}
}

func TestExportConcurrentChangesPreserveAlreadyValidatedRuns(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, runFixture)
	}{
		{"run-publication", func(t *testing.T, f runFixture) { copyExportRun(t, f, f.taskID, "run-published-later") }},
		{"task-publication", func(t *testing.T, f runFixture) {
			task := readTestObject(t, f.taskPath)
			task["id"] = "LATER"
			writeTestJSON(t, filepath.Join(filepath.Dir(f.taskPath), "LATER.json"), task)
		}},
		{"directory-replacement", func(t *testing.T, f runFixture) {
			moved := filepath.Join(filepath.Dir(f.runPath), ".tmp-retired-run")
			if err := os.Rename(f.runPath, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(f.runPath, os.DirFS(moved)); err != nil {
				t.Fatal(err)
			}
		}},
		{"same-size-metadata-write-restored-time", func(t *testing.T, f runFixture) {
			path := filepath.Join(f.runPath, "checks.json")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.Replace(contents, []byte("0.1"), []byte("0.2"), 1)
			if bytes.Equal(contents, changed) {
				t.Fatal("fixture duration missing")
			}
			if err := os.WriteFile(path, changed, info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
		{"completion-publication", func(t *testing.T, f runFixture) { writeExportCompletion(t, f) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			report, err := exportRunsWithHooks(fixture.repository, "test", exportHooks{afterScan: func() { tc.mutate(t, fixture) }})
			if err != nil || report.ScanComplete || len(report.Runs) != 1 || report.Runs[0].RunID != fixture.runID || !hasExportIssue(report, "concurrent_change") {
				t.Fatalf("concurrent export = %#v, %v", report, err)
			}
			encoded, _ := json.Marshal(report)
			if bytes.Contains(encoded, []byte(fixture.repository)) {
				t.Fatal("concurrency issue exposed repository path")
			}
		})
	}
}

func TestExportDetectsPublicationIntoInitiallyEmptyRepository(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := exportRunsWithHooks(repository, "test", exportHooks{afterScan: func() {
		if err := os.MkdirAll(filepath.Join(repository, ".seal", "tasks"), 0o700); err != nil {
			t.Fatal(err)
		}
	}})
	if err != nil || report.ScanComplete || !hasExportIssue(report, "concurrent_change") {
		t.Fatalf("initially empty concurrent export = %#v, %v", report, err)
	}
}

func hasExportIssue(report *ExportReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func writeExportCompletion(t *testing.T, fixture runFixture) {
	t.Helper()
	validated, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := renderCompletionRecord(validated, validated.sourceAfterSHA256, time.Date(2026, 9, 11, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runPath, "completion.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func refreshExportManifest(t *testing.T, fixture runFixture) {
	t.Helper()
	manifest := readTestObject(t, filepath.Join(fixture.runPath, "run-manifest.json"))
	files := make([]string, 0)
	for _, record := range manifest["files"].([]any) {
		files = append(files, record.(map[string]any)["path"].(string))
	}
	writeManifest(t, fixture.runPath, fixture.taskID, fixture.runID, files)
}

func copyExportRun(t *testing.T, fixture runFixture, taskID, runID string) {
	t.Helper()
	path := filepath.Join(fixture.repository, ".seal", "evidence", taskID, runID)
	if err := os.CopyFS(path, os.DirFS(fixture.runPath)); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(fixture.repository, ".seal", "tasks", taskID+".json")
	task := readTestObject(t, fixture.taskPath)
	task["id"] = taskID
	writeTestJSON(t, taskPath, task)
	writeTestJSON(t, filepath.Join(path, "task.json"), task)
	mutateTestJSON(t, filepath.Join(path, "verification.json"), func(document map[string]any) {
		document["task_id"], document["run_id"] = taskID, runID
	})
	refreshExportManifest(t, runFixture{repository: fixture.repository, runPath: path, taskPath: taskPath, taskID: taskID, runID: runID})
}

func exportFileTimes(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	result := make(map[string]time.Time)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[path] = info.ModTime()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
