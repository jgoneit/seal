package runstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	fixtureTaskID = "TASK-RUN-STATE"
	fixtureRunID  = "run-0001"
	fixtureBase   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fixtureOptions struct {
	required       bool
	passed         bool
	timedOut       bool
	exitCode       any
	scopeViolation bool
	sourceStable   bool
}

type runFixture struct {
	repository string
	runPath    string
	taskPath   string
	taskID     string
	runID      string
}

func TestValidateRunAndDetachedSummary(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{
		required:     true,
		passed:       true,
		exitCode:     0,
		sourceStable: true,
	})
	nested := filepath.Join(fixture.repository, "nested", "directory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	validated, err := ValidateRun(nested, fixture.taskID, fixture.runID)
	if err != nil {
		t.Fatalf("ValidateRun() error = %v", err)
	}
	summary := validated.Summary()
	if summary.SchemaVersion != 1 || summary.TaskID != fixture.taskID || summary.RunID != fixture.runID {
		t.Fatalf("unexpected identity summary: %#v", summary)
	}
	if summary.MechanicalResult != "pass" || !summary.ScopePass || !summary.RequiredChecksPass || !summary.SourceStableDuringChecks {
		t.Fatalf("unexpected pass summary: %#v", summary)
	}
	if len(summary.Checks) != 1 || summary.Checks[0].ExitCode == nil || string(*summary.Checks[0].ExitCode) != "0" {
		t.Fatalf("unexpected checks: %#v", summary.Checks)
	}
	if len(summary.ScopeViolations) != 0 {
		t.Fatalf("scope violations = %#v", summary.ScopeViolations)
	}

	// A caller cannot mutate the value retained by the validated authority.
	summary.Checks[0].Name = "changed"
	summary.Checks = append(summary.Checks, Check{})
	again := validated.Summary()
	if len(again.Checks) != 1 || again.Checks[0].Name != "unit-test" {
		t.Fatalf("Summary() retained caller mutation: %#v", again.Checks)
	}

	encoded, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	prefix := `{"checks":[{"exit_code":0,"name":"unit-test","passed":true,"required":true,"timed_out":false}],"evidence_sha256":`
	if !strings.HasPrefix(string(encoded), prefix) {
		t.Fatalf("summary field order/content = %s", encoded)
	}
}

func TestValidFailedRunsRemainStoredState(t *testing.T) {
	tests := []struct {
		name         string
		options      fixtureOptions
		mechanical   string
		requiredPass bool
		scopePass    bool
		stable       bool
		expectedExit string
	}{
		{
			name:       "required failure",
			options:    fixtureOptions{required: true, passed: false, exitCode: 23, sourceStable: true},
			mechanical: "fail", requiredPass: false, scopePass: true, stable: true, expectedExit: "23",
		},
		{
			name:       "optional failure",
			options:    fixtureOptions{required: false, passed: false, exitCode: 7, sourceStable: true},
			mechanical: "pass", requiredPass: true, scopePass: true, stable: true, expectedExit: "7",
		},
		{
			name:       "timeout",
			options:    fixtureOptions{required: true, passed: false, timedOut: true, exitCode: 24, sourceStable: true},
			mechanical: "fail", requiredPass: false, scopePass: true, stable: true, expectedExit: "24",
		},
		{
			name:       "launch failure",
			options:    fixtureOptions{required: true, passed: false, exitCode: nil, sourceStable: true},
			mechanical: "fail", requiredPass: false, scopePass: true, stable: true,
		},
		{
			name:       "scope violation",
			options:    fixtureOptions{required: true, passed: true, exitCode: 0, scopeViolation: true, sourceStable: true},
			mechanical: "fail", requiredPass: true, scopePass: false, stable: true, expectedExit: "0",
		},
		{
			name:       "source instability",
			options:    fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: false},
			mechanical: "fail", requiredPass: true, scopePass: true, stable: false, expectedExit: "0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, test.options)
			validated, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
			if err != nil {
				t.Fatalf("ValidateRun() error = %v", err)
			}
			summary := validated.Summary()
			if summary.MechanicalResult != test.mechanical || summary.RequiredChecksPass != test.requiredPass || summary.ScopePass != test.scopePass || summary.SourceStableDuringChecks != test.stable {
				t.Fatalf("summary = %#v", summary)
			}
			if test.expectedExit == "" {
				if summary.Checks[0].ExitCode != nil {
					t.Fatalf("exit code = %v, want nil", summary.Checks[0].ExitCode)
				}
			} else if summary.Checks[0].ExitCode == nil || string(*summary.Checks[0].ExitCode) != test.expectedExit {
				t.Fatalf("exit code = %v, want %s", summary.Checks[0].ExitCode, test.expectedExit)
			}
		})
	}
}

func TestErrorKinds(t *testing.T) {
	valid := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	outside := t.TempDir()
	tests := []struct {
		name   string
		cwd    string
		taskID string
		runID  string
		kind   ErrorKind
		typeOf any
	}{
		{"invalid task", valid.repository, "../TASK", valid.runID, KindInvalidInput, &IdentityError{}},
		{"invalid run", valid.repository, valid.taskID, "../run", KindInvalidInput, &IdentityError{}},
		{"missing task", valid.repository, "TASK-MISSING", valid.runID, KindInvalidInput, &IdentityError{}},
		{"outside repository", outside, valid.taskID, valid.runID, KindRepository, &RepositoryError{}},
		{"missing run", valid.repository, valid.taskID, "missing-run", KindEvidence, &EvidenceError{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateRun(test.cwd, test.taskID, test.runID)
			if err == nil {
				t.Fatal("ValidateRun() succeeded")
			}
			if got := KindOf(err); got != test.kind {
				t.Fatalf("KindOf(%v) = %v, want %v", err, got, test.kind)
			}
			switch target := test.typeOf.(type) {
			case *IdentityError:
				if !errors.As(err, &target) {
					t.Fatalf("error type = %T", err)
				}
			case *RepositoryError:
				if !errors.As(err, &target) {
					t.Fatalf("error type = %T", err)
				}
			case *EvidenceError:
				if !errors.As(err, &target) {
					t.Fatalf("error type = %T", err)
				}
			}
		})
	}
	if KindOf(errors.New("other")) != KindUnknown {
		t.Fatal("unknown error was categorized")
	}
}

func TestTaskMinimumValidationOccursAtRunBoundary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		kind   ErrorKind
	}{
		{"unsupported schema", func(task map[string]any) { task["schema_version"] = 99 }, KindInvalidInput},
		{"id mismatch", func(task map[string]any) { task["id"] = "OTHER" }, KindInvalidInput},
		{"missing baseline", func(task map[string]any) { delete(task, "baseline") }, KindInvalidInput},
		{"empty scope", func(task map[string]any) { task["scope"] = []any{} }, KindInvalidInput},
		{"unsafe scope", func(task map[string]any) { task["scope"] = []any{"../src"} }, KindEvidence},
		{"empty checks", func(task map[string]any) { task["checks"] = []any{} }, KindInvalidInput},
		{"invalid verifier", func(task map[string]any) { task["verifier"] = map[string]any{} }, KindInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			task := readTestObject(t, fixture.taskPath)
			test.mutate(task)
			writeTestJSON(t, fixture.taskPath, task)
			_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
			if err == nil || KindOf(err) != test.kind {
				t.Fatalf("error = %v, kind = %v, want %v", err, KindOf(err), test.kind)
			}
		})
	}
}

func TestEvidenceTamperAndVersionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, runFixture)
	}{
		{"unsupported verification version", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "verification.json"), func(value map[string]any) { value["schema_version"] = 1 })
		}},
		{"forged check outcome", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "checks.json"), func(value map[string]any) {
				value["checks"].([]any)[0].(map[string]any)["passed"] = false
			})
		}},
		{"forged aggregate", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "verification.json"), func(value map[string]any) { value["mechanical_result"] = "fail" })
		}},
		{"source snapshot tamper", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "source-before-checks.json"), func(value map[string]any) { value["baseline"] = strings.Repeat("b", 40) })
		}},
		{"unsafe listed path", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "verification.json"), func(value map[string]any) { value["evidence_files"].([]any)[0] = "../outside" })
		}},
		{"manifest size", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "run-manifest.json"), func(value map[string]any) { value["files"].([]any)[0].(map[string]any)["size_bytes"] = 0 })
		}},
		{"manifest hash", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "run-manifest.json"), func(value map[string]any) {
				value["files"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64)
			})
		}},
		{"manifest evidence digest", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "run-manifest.json"), func(value map[string]any) { value["evidence_sha256"] = strings.Repeat("0", 64) })
		}},
		{"missing file", func(t *testing.T, fixture runFixture) {
			if err := os.Remove(filepath.Join(fixture.runPath, "diff.patch")); err != nil {
				t.Fatal(err)
			}
		}},
		{"task snapshot mismatch", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "task.json"), func(value map[string]any) { value["objective"] = "different" })
		}},
		{"run identity mismatch", func(t *testing.T, fixture runFixture) {
			mutateTestJSON(t, filepath.Join(fixture.runPath, "verification.json"), func(value map[string]any) { value["run_id"] = "other-run" })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			test.mutate(t, fixture)
			_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
			if err == nil {
				t.Fatal("ValidateRun() succeeded after tamper")
			}
			if got := KindOf(err); got != KindEvidence && !(test.name == "task snapshot mismatch" || test.name == "run identity mismatch") {
				t.Fatalf("KindOf(%v) = %v", err, got)
			}
			if (test.name == "task snapshot mismatch" || test.name == "run identity mismatch") && KindOf(err) != KindInvalidInput {
				t.Fatalf("identity KindOf(%v) = %v", err, KindOf(err))
			}
		})
	}
}

func TestConfinedArtifactSymlinkPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires POSIX semantics")
	}
	t.Run("confined regular target accepted", func(t *testing.T) {
		fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
		logPath := filepath.Join(fixture.runPath, "checks", "000-unit-test.stdout")
		contents, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.runPath, "confined-target.log")
		if err := os.WriteFile(target, contents, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(logPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../confined-target.log", logPath); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID); err != nil {
			t.Fatalf("confined symlink rejected: %v", err)
		}
	})

	tests := []struct {
		name   string
		mutate func(*testing.T, runFixture)
	}{
		{"outside escape", func(t *testing.T, fixture runFixture) {
			log := filepath.Join(fixture.runPath, "checks", "000-unit-test.stdout")
			outside := filepath.Join(t.TempDir(), "outside.log")
			if err := os.WriteFile(outside, []byte("ok\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, log); err != nil {
				t.Fatal(err)
			}
		}},
		{"broken", func(t *testing.T, fixture runFixture) {
			log := filepath.Join(fixture.runPath, "checks", "000-unit-test.stdout")
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing-target", log); err != nil {
				t.Fatal(err)
			}
		}},
		{"loop", func(t *testing.T, fixture runFixture) {
			log := filepath.Join(fixture.runPath, "checks", "000-unit-test.stdout")
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("000-unit-test.stdout", log); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-file", func(t *testing.T, fixture runFixture) {
			log := filepath.Join(fixture.runPath, "checks", "000-unit-test.stdout")
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(".", log); err != nil {
				t.Fatal(err)
			}
		}},
		{"target content mismatch", func(t *testing.T, fixture runFixture) {
			log := filepath.Join(fixture.runPath, "checks", "000-unit-test.stdout")
			target := filepath.Join(fixture.runPath, "changed-target.log")
			if err := os.WriteFile(target, []byte("different\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(log); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../changed-target.log", log); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			test.mutate(t, fixture)
			_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
			if err == nil || KindOf(err) != KindEvidence {
				t.Fatalf("error = %v, kind = %v", err, KindOf(err))
			}
		})
	}

	t.Run("run directory symlink rejected", func(t *testing.T) {
		fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
		outside := filepath.Join(t.TempDir(), "run")
		if err := os.Rename(fixture.runPath, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, fixture.runPath); err != nil {
			t.Fatal(err)
		}
		_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
		if err == nil || KindOf(err) != KindEvidence {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestArtifactFilesystemPathUsesPythonSurrogateEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw-byte filename fixture requires POSIX")
	}
	runDirectory := t.TempDir()
	decoded, err := decodeJSONObject([]byte(`{"path":"checks/raw-\udcff.stdout"}`))
	if err != nil {
		t.Fatal(err)
	}
	logical := decoded["path"].(string)
	filesystemRelative, err := surrogateEscapeFilesystemPath(logical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filesystemRelative, string([]byte{0xff})) {
		t.Fatalf("filesystem path did not contain raw surrogateescape byte: %q", filesystemRelative)
	}
	unsupported, err := decodeJSONObject([]byte(`{"path":"checks/raw-\ud800.stdout"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact(runDirectory, unsupported["path"].(string)); err == nil {
		t.Fatal("readArtifact() accepted unsupported lone surrogate")
	}
	checksDirectory := filepath.Join(runDirectory, "checks")
	if err := os.Mkdir(checksDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(checksDirectory, "raw-"+string([]byte{0xff})+".stdout")
	if err := os.WriteFile(physical, []byte("raw-byte\n"), 0o644); err != nil {
		t.Logf("filesystem does not support raw-byte fixture: %v", err)
		return
	}
	contents, err := readArtifact(runDirectory, logical)
	if err != nil {
		t.Fatalf("readArtifact() error = %v", err)
	}
	if string(contents) != "raw-byte\n" {
		t.Fatalf("readArtifact() = %q", contents)
	}
}

func TestPhysicalConsumerFilesAreIgnoredAndQueryDoesNotWrite(t *testing.T) {
	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	for _, name := range []string{"verdict.raw.json", "verdict.json", "completion.json", "unlisted-extra"} {
		if err := os.WriteFile(filepath.Join(fixture.runPath, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotTree(t, fixture.repository)
	if _, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID); err != nil {
		t.Fatalf("ValidateRun() error = %v", err)
	}
	after := snapshotTree(t, fixture.repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("repository changed\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestPythonCompatibleCanonicalJSONVectors(t *testing.T) {
	snapshot := map[string]any{
		"schema_version": json.Number("1"),
		"baseline":       fixtureBase,
		"entries": []any{map[string]any{
			"path": "src/café😀.txt", "state": "present", "mode": "100644",
			"size_bytes": json.Number("3"), "sha256": strings.Repeat("b", 64),
		}},
	}
	manifest := map[string]any{
		"schema_version": json.Number("1"), "task_id": "TASK-UNICODE", "run_id": "run-1",
		"files": []any{map[string]any{
			"path": "checks/é<&😀.stdout", "size_bytes": json.Number("3"), "sha256": strings.Repeat("b", 64),
		}},
	}
	tests := []struct {
		name      string
		value     any
		asciiOnly bool
		digest    string
	}{
		{"source ensure_ascii true", snapshot, true, "f77980a8d39b3757c86a84c5a255044ff05005d17174f2657e0963bb4b810ac3"},
		{"manifest ensure_ascii false", manifest, false, "c69f7f42904ad8c92bbcd0c9b05e1daf9693d086992fefd21b0f7a47df4b99fc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := canonicalJSON(test.value, test.asciiOnly)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(encoded)
			if got := hex.EncodeToString(digest[:]); got != test.digest {
				t.Fatalf("digest = %s, want %s\njson=%s", got, test.digest, encoded)
			}
		})
	}
}

func TestReferenceJSONUsesFrozenPrettyFormat(t *testing.T) {
	exitCode := json.Number("0")
	summary := Summary{
		Checks:         []Check{{ExitCode: &exitCode, Name: "unit", Passed: true, Required: true, TimedOut: false}},
		EvidenceSHA256: "digest", MechanicalResult: "pass", RequiredChecksPass: true,
		RunID: "run-1", SchemaVersion: 1, ScopePass: true, ScopeViolations: []ScopeViolation{},
		SourceStableDuringChecks: true, TaskID: "TASK-1",
	}
	got, err := summary.ReferenceJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"checks\": [\n" +
		"    {\n" +
		"      \"exit_code\": 0,\n" +
		"      \"name\": \"unit\",\n" +
		"      \"passed\": true,\n" +
		"      \"required\": true,\n" +
		"      \"timed_out\": false\n" +
		"    }\n" +
		"  ],\n" +
		"  \"evidence_sha256\": \"digest\",\n" +
		"  \"mechanical_result\": \"pass\",\n" +
		"  \"required_checks_pass\": true,\n" +
		"  \"run_id\": \"run-1\",\n" +
		"  \"schema_version\": 1,\n" +
		"  \"scope_pass\": true,\n" +
		"  \"scope_violations\": [],\n" +
		"  \"source_stable_during_checks\": true,\n" +
		"  \"task_id\": \"TASK-1\"\n" +
		"}"
	if string(got) != want {
		t.Fatalf("ReferenceJSON() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestPythonConstantsDoNotBecomeObjectKeys(t *testing.T) {
	for _, document := range []string{`{NaN:1}`, `{Infinity:1}`, `{-Infinity:1}`} {
		if _, err := decodeJSONObject([]byte(document)); err == nil {
			t.Fatalf("decodeJSONObject(%q) succeeded", document)
		}
	}
	// An actual escaped-NUL string key is still distinct from the internal marker.
	if _, err := decodeJSONObject([]byte(`{"\u0000fNaN":1}`)); err != nil {
		t.Fatalf("literal marker-like key rejected: %v", err)
	}
}

func TestReferenceJSONRejectsUnsupportedLoneSurrogate(t *testing.T) {
	decoded, err := decodeJSONObject([]byte(`{"name":"\ud800"}`))
	if err != nil {
		t.Fatal(err)
	}
	summary := Summary{
		Checks:          []Check{{Name: decoded["name"].(string), Required: true}},
		ScopeViolations: []ScopeViolation{},
	}
	if _, err := summary.ReferenceJSON(); err == nil {
		t.Fatal("ReferenceJSON() accepted unsupported lone surrogate")
	}
	if _, err := json.Marshal(summary); err != nil {
		t.Fatalf("safe MarshalJSON rejected escaped surrogate: %v", err)
	}
}

func TestEscapedLoneSurrogateRemainsValidAndLossless(t *testing.T) {
	decoded, err := decodeJSONObject([]byte(`{"path":"\udcff"}`))
	if err != nil {
		t.Fatalf("decodeJSONObject() error = %v", err)
	}
	path := decoded["path"].(string)
	encoded, err := canonicalJSON(map[string]any{"path": path}, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"path":"\udcff"}` {
		t.Fatalf("canonical surrogate JSON = %q", encoded)
	}
	digest := sha256.Sum256(encoded)
	if got := hex.EncodeToString(digest[:]); got != "440f0f9185f2195efc33694e622f8584a3c4f8ce8cdb47a254b125848589f424" {
		t.Fatalf("surrogate digest = %s", got)
	}

	fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
	changedPath := "docs/" + path
	change := map[string]any{
		"source": "untracked", "status": "untracked", "path": changedPath, "previous_path": nil,
		"old_mode": nil, "new_mode": "100644", "mode_changed": false, "is_binary": false, "in_scope": false,
	}
	changed := map[string]any{
		"schema_version": 1, "baseline": fixtureBase, "scope": []any{"src"}, "changes": []any{change},
	}
	writeTestCanonicalJSON(t, filepath.Join(fixture.runPath, "changed-files.json"), changed, true)

	entries := []any{map[string]any{
		"path": changedPath, "state": "present", "mode": "100644", "size_bytes": 1,
		"sha256": strings.Repeat("b", 64),
	}}
	source := sourceDocument(t, fixtureBase, entries)
	writeTestCanonicalJSON(t, filepath.Join(fixture.runPath, "source-before-checks.json"), source, true)
	writeTestCanonicalJSON(t, filepath.Join(fixture.runPath, "source-after-checks.json"), source, true)
	verification := readTestObject(t, filepath.Join(fixture.runPath, "verification.json"))
	verification["changed_files"] = []any{change}
	verification["scope_pass"] = false
	verification["scope_violations"] = []any{change}
	verification["mechanical_result"] = "fail"
	verification["source_before_checks_sha256"] = source["snapshot_sha256"]
	verification["source_after_checks_sha256"] = source["snapshot_sha256"]
	writeTestCanonicalJSON(t, filepath.Join(fixture.runPath, "verification.json"), verification, true)
	writeManifest(t, fixture.runPath, fixture.taskID, fixture.runID, []string{
		"task.json", "source-before-checks.json", "source-after-checks.json", "changed-files.json",
		"diff.patch", "checks.json", "checks/000-unit-test.stdout", "checks/000-unit-test.stderr", "verification.json",
	})

	validated, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
	if err != nil {
		t.Fatalf("ValidateRun() error = %v", err)
	}
	summary := validated.Summary()
	if len(summary.ScopeViolations) != 1 || summary.ScopeViolations[0].Path != changedPath {
		t.Fatalf("scope violations = %#v", summary.ScopeViolations)
	}
	publicJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(publicJSON), `"path":"docs/\udcff"`) || strings.Contains(string(publicJSON), "�") {
		t.Fatalf("public summary lost surrogate: %s", publicJSON)
	}
	referenceJSON, err := summary.ReferenceJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(referenceJSON), "\"path\": \"docs/\xff\"") {
		t.Fatalf("reference summary did not restore surrogateescape byte: %q", referenceJSON)
	}
}

func TestSavedTaskEqualityUsesPythonNumberSemantics(t *testing.T) {
	tests := []struct {
		name     string
		saved    string
		evidence string
		equal    bool
	}{
		{"rounded decimals", "0.1", "0.10000000000000001", true},
		{"float underflow", "0", "1e-400", true},
		{"float overflow", "1e400", "1e500", true},
		{"rounded integer float", "9007199254740992", "9007199254740993.0", true},
		{"boolean is integer subclass", "1", "true", true},
		{"negative zero", "-0", "0", true},
		{"integer and integral float", "1", "1.0", true},
		{"infinities", "Infinity", "Infinity", true},
		{"overflow and infinity", "1e400", "Infinity", true},
		{"decoder NaN singleton identity", "NaN", "NaN", true},
		{"unrepresentable integer differs", "9007199254740993", "9007199254740993.0", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			appendRawJSONField(t, fixture.taskPath, "compat_extra", test.saved)
			appendRawJSONField(t, filepath.Join(fixture.runPath, "task.json"), "compat_extra", test.evidence)
			writeManifest(t, fixture.runPath, fixture.taskID, fixture.runID, []string{
				"task.json", "source-before-checks.json", "source-after-checks.json", "changed-files.json",
				"diff.patch", "checks.json", "checks/000-unit-test.stdout", "checks/000-unit-test.stderr", "verification.json",
			})
			_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
			if test.equal && err != nil {
				t.Fatalf("equal Python values rejected: %v", err)
			}
			if !test.equal && (err == nil || KindOf(err) != KindInvalidInput) {
				t.Fatalf("unequal Python values error = %v, kind = %v", err, KindOf(err))
			}
		})
	}
}

func TestPythonIntegerDigitLimitParity(t *testing.T) {
	limit := strings.Repeat("9", pythonIntegerDigitLimit)
	overLimit := strings.Repeat("9", pythonIntegerDigitLimit+1)

	t.Run("decoder boundary", func(t *testing.T) {
		tests := []struct {
			name        string
			token       string
			wantRuntime bool
		}{
			{"exactly 4300 digits", limit, false},
			{"minus is excluded", "-" + limit, false},
			{"4301 digits", overLimit, true},
			{"negative 4301 digits", "-" + overLimit, true},
			{"decimal token is unaffected", overLimit + ".0", false},
			{"exponent token is unaffected", "1e" + overLimit, false},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				_, err := decodeJSONObject([]byte(`{"value":` + test.token + `}`))
				if test.wantRuntime {
					requireRuntimeError(t, err)
					return
				}
				if err != nil {
					t.Fatalf("decodeJSONObject() error = %v", err)
				}
			})
		}
	})

	t.Run("matching 4300 digit Task extra passes", func(t *testing.T) {
		fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
		appendRawJSONField(t, fixture.taskPath, "compat_extra", limit)
		appendRawJSONField(t, filepath.Join(fixture.runPath, "task.json"), "compat_extra", limit)
		writeManifest(t, fixture.runPath, fixture.taskID, fixture.runID, []string{
			"task.json", "source-before-checks.json", "source-after-checks.json", "changed-files.json",
			"diff.patch", "checks.json", "checks/000-unit-test.stdout", "checks/000-unit-test.stderr", "verification.json",
		})
		if _, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID); err != nil {
			t.Fatalf("ValidateRun() rejected 4300-digit matching Task fields: %v", err)
		}
	})

	t.Run("saved Task 4301 digit extra is Runtime", func(t *testing.T) {
		fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
		appendRawJSONField(t, fixture.taskPath, "compat_extra", overLimit)
		_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
		requireRuntimeError(t, err)
	})

	t.Run("Evidence Task 4301 digit extra is Runtime", func(t *testing.T) {
		fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
		appendRawJSONField(t, filepath.Join(fixture.runPath, "task.json"), "compat_extra", overLimit)
		_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
		requireRuntimeError(t, err)
	})

	t.Run("Evidence documents propagate Runtime", func(t *testing.T) {
		for _, relativePath := range []string{"checks.json", "source-before-checks.json"} {
			t.Run(relativePath, func(t *testing.T) {
				fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
				replaceRawJSONField(t, filepath.Join(fixture.runPath, relativePath), "schema_version", "1", overLimit)
				_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
				requireRuntimeError(t, err)
			})
		}
	})

	t.Run("manifest 4301 digit field is Runtime", func(t *testing.T) {
		fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
		replaceRawJSONField(t, filepath.Join(fixture.runPath, "run-manifest.json"), "schema_version", "1", overLimit)
		_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
		requireRuntimeError(t, err)
	})
}

func TestIntegerLimitPreservesHandledMalformedJSONCategories(t *testing.T) {
	tests := []struct {
		name string
		path func(runFixture) string
		kind ErrorKind
	}{
		{"saved Task", func(fixture runFixture) string { return fixture.taskPath }, KindInvalidInput},
		{"Evidence document", func(fixture runFixture) string { return filepath.Join(fixture.runPath, "checks.json") }, KindEvidence},
		{"manifest", func(fixture runFixture) string { return filepath.Join(fixture.runPath, "run-manifest.json") }, KindEvidence},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, fixtureOptions{required: true, passed: true, exitCode: 0, sourceStable: true})
			if err := os.WriteFile(test.path(fixture), []byte("{\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ValidateRun(fixture.repository, fixture.taskID, fixture.runID)
			if err == nil || KindOf(err) != test.kind {
				t.Fatalf("error = %v, kind = %v, want %v", err, KindOf(err), test.kind)
			}
		})
	}
}

func newRunFixture(t *testing.T, options fixtureOptions) runFixture {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(repository, ".seal", "evidence", fixtureTaskID, fixtureRunID)
	if err := os.MkdirAll(filepath.Join(runPath, "checks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !options.passed && options.exitCode == nil && options.timedOut {
		options.exitCode = 24
	}

	task := map[string]any{
		"schema_version": 1,
		"id":             fixtureTaskID,
		"type":           "test",
		"objective":      "Validate one stored Run.",
		"scope":          []any{"src"},
		"checks": []any{map[string]any{
			"name": "unit-test", "argv": []any{"go", "test", "./..."}, "required": options.required,
		}},
		"risk":     "low",
		"verifier": map[string]any{"required": false},
		"baseline": fixtureBase,
	}
	taskPath := filepath.Join(repository, ".seal", "tasks", fixtureTaskID+".json")
	writeTestJSON(t, taskPath, task)
	writeTestJSON(t, filepath.Join(runPath, "task.json"), task)

	changes := []any{}
	productChanges := []any{}
	violations := []any{}
	if options.scopeViolation {
		change := map[string]any{
			"source": "untracked", "status": "untracked", "path": "docs/outside.txt", "previous_path": nil,
			"old_mode": nil, "new_mode": "100644", "mode_changed": false, "is_binary": false, "in_scope": false,
		}
		changes = append(changes, change)
		productChanges = append(productChanges, change)
		violations = append(violations, change)
	}
	changed := map[string]any{"schema_version": 1, "baseline": fixtureBase, "scope": []any{"src"}, "changes": changes}
	writeTestJSON(t, filepath.Join(runPath, "changed-files.json"), changed)
	if err := os.WriteFile(filepath.Join(runPath, "diff.patch"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	stdoutPath := "checks/000-unit-test.stdout"
	stderrPath := "checks/000-unit-test.stderr"
	if err := os.WriteFile(filepath.Join(runPath, filepath.FromSlash(stdoutPath)), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runPath, filepath.FromSlash(stderrPath)), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	checks := map[string]any{
		"schema_version": 1,
		"checks": []any{map[string]any{
			"name": "unit-test", "argv": []any{"go", "test", "./..."}, "cwd": repository,
			"started_at": "2026-08-15T00:00:00.000000Z", "finished_at": "2026-08-15T00:00:00.100000Z",
			"duration_seconds": 0.1, "effective_timeout": 300, "exit_code": options.exitCode,
			"timed_out": options.timedOut, "stdout_path": stdoutPath, "stderr_path": stderrPath,
			"required": options.required, "passed": options.passed,
		}},
	}
	writeTestJSON(t, filepath.Join(runPath, "checks.json"), checks)

	beforeEntries := []any{}
	afterEntries := beforeEntries
	if !options.sourceStable {
		afterEntries = []any{map[string]any{
			"path": "src/example.txt", "state": "present", "mode": "100644", "size_bytes": 6,
			"sha256": strings.Repeat("b", 64),
		}}
	}
	before := sourceDocument(t, fixtureBase, beforeEntries)
	after := sourceDocument(t, fixtureBase, afterEntries)
	writeTestJSON(t, filepath.Join(runPath, "source-before-checks.json"), before)
	writeTestJSON(t, filepath.Join(runPath, "source-after-checks.json"), after)

	requiredPass := !options.required || options.passed
	scopePass := !options.scopeViolation
	mechanical := "fail"
	if requiredPass && scopePass && options.sourceStable {
		mechanical = "pass"
	}
	evidenceFiles := []any{
		"task.json", "source-before-checks.json", "source-after-checks.json", "changed-files.json",
		"diff.patch", "checks.json", stdoutPath, stderrPath, "verification.json",
	}
	verification := map[string]any{
		"schema_version": 2, "task_id": fixtureTaskID, "run_id": fixtureRunID, "baseline": fixtureBase,
		"changed_files": productChanges, "scope_pass": scopePass, "scope_violations": violations,
		"required_checks_pass": requiredPass, "mechanical_result": mechanical, "evidence_files": evidenceFiles,
		"timestamp": "2026-08-15T00:00:00.200000Z", "duration": 0.2,
		"source_snapshot_schema_version": 1,
		"source_before_checks_sha256":    before["snapshot_sha256"], "source_after_checks_sha256": after["snapshot_sha256"],
		"source_stable_during_checks": options.sourceStable,
	}
	writeTestJSON(t, filepath.Join(runPath, "verification.json"), verification)
	writeManifest(t, runPath, fixtureTaskID, fixtureRunID, stringSlice(evidenceFiles))
	return runFixture{repository: repository, runPath: runPath, taskPath: taskPath, taskID: fixtureTaskID, runID: fixtureRunID}
}

func sourceDocument(t *testing.T, baseline string, entries []any) map[string]any {
	t.Helper()
	payload := map[string]any{"schema_version": json.Number("1"), "baseline": baseline, "entries": entries}
	canonical, err := canonicalJSON(payload, true)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return map[string]any{"schema_version": 1, "baseline": baseline, "entries": entries, "snapshot_sha256": hex.EncodeToString(digest[:])}
}

func writeManifest(t *testing.T, runPath, taskID, runID string, files []string) {
	t.Helper()
	sort.Strings(files)
	records := make([]any, len(files))
	for index, path := range files {
		contents, err := os.ReadFile(filepath.Join(runPath, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		records[index] = map[string]any{"path": path, "size_bytes": len(contents), "sha256": hex.EncodeToString(digest[:])}
	}
	payload := map[string]any{"schema_version": json.Number("1"), "task_id": taskID, "run_id": runID, "files": records}
	canonical, err := canonicalJSON(payload, false)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	document := map[string]any{
		"schema_version": 1, "task_id": taskID, "run_id": runID, "files": records,
		"evidence_sha256": hex.EncodeToString(digest[:]), "created_at": "2026-08-15T00:00:00.300000Z",
	}
	writeTestJSON(t, filepath.Join(runPath, "run-manifest.json"), document)
}

func stringSlice(values []any) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.(string)
	}
	return result
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestCanonicalJSON(t *testing.T, path string, value any, asciiOnly bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents, err := canonicalJSON(value, asciiOnly)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestObject(t *testing.T, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mutateTestJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	value := readTestObject(t, path)
	mutate(value)
	writeTestJSON(t, path, value)
}

func appendRawJSONField(t *testing.T, path, name, rawValue string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(contents))
	if !strings.HasSuffix(trimmed, "}") {
		t.Fatalf("%s is not a JSON object", path)
	}
	updated := strings.TrimSuffix(trimmed, "}") + ",\n  " + strconv.Quote(name) + ": " + rawValue + "\n}\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceRawJSONField(t *testing.T, path, name, oldValue, newValue string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := strconv.Quote(name) + ": " + oldValue
	replacement := strconv.Quote(name) + ": " + newValue
	if !strings.Contains(string(contents), needle) {
		t.Fatalf("%s does not contain raw field %s", path, name)
	}
	updated := strings.Replace(string(contents), needle, replacement, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireRuntimeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded, want Runtime error")
	}
	if got := KindOf(err); got != KindRuntime {
		t.Fatalf("KindOf(%v) = %v, want %v", err, got, KindRuntime)
	}
	var runtimeError *RuntimeError
	if !errors.As(err, &runtimeError) {
		t.Fatalf("error type = %T, want *RuntimeError", err)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[filepath.ToSlash(relative)+"/"] = "directory"
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[filepath.ToSlash(relative)] = "symlink:" + target
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		result[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
