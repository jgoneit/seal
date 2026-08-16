package runstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var completionTestTime = time.Date(2026, 8, 16, 7, 8, 9, 123456000, time.UTC)

func TestCompletionExitCodeClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "runtime", err: &RuntimeError{message: "runtime"}, want: 1},
		{name: "input", err: &IdentityError{message: "input"}, want: 2},
		{name: "repository", err: &RepositoryError{message: "repository"}, want: 3},
		{name: "scope", err: &completionPolicyError{exitCode: 4, message: "scope"}, want: 4},
		{name: "check", err: &completionPolicyError{exitCode: 5, message: "check"}, want: 5},
		{name: "timeout", err: &completionPolicyError{exitCode: 6, message: "timeout"}, want: 6},
		{name: "verifier", err: &completionPolicyError{exitCode: 7, message: "verifier"}, want: 7},
		{name: "evidence", err: &EvidenceError{message: "evidence"}, want: 8},
		{name: "source", err: &completionPolicyError{exitCode: 9, message: "source"}, want: 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := CompletionExitCode(test.err)
			if !ok || got != test.want {
				t.Fatalf("CompletionExitCode(%v) = %d, %v; want %d, true", test.err, got, ok, test.want)
			}
		})
	}
	if got, ok := CompletionExitCode(errors.New("unknown")); ok || got != 0 {
		t.Fatalf("unknown CompletionExitCode = %d, %v", got, ok)
	}
}

func TestCompleteWritesImmutableV2AndRechecksCurrentEligibility(t *testing.T) {
	repository, taskID, run := passingCompletionRun(t)
	for _, name := range []string{"verdict.raw.json", "verdict.json"} {
		if err := os.WriteFile(filepath.Join(run.EvidencePath, name), []byte("not verdict JSON\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	completed, err := completeWithHooks(repository, taskID, run.RunID, completeHooks{
		now: func() time.Time { return completionTestTime },
	})
	if err != nil {
		t.Fatalf("Complete(): %v", err)
	}
	wantPath := filepath.Join(run.EvidencePath, "completion.json")
	if completed.TaskID != taskID || completed.RunID != run.RunID || completed.CompletionPath != wantPath {
		t.Fatalf("completion result = %#v", completed)
	}
	encoded, err := completed.ReferenceJSON()
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected) != 3 || projected["completion_path"] != wantPath || projected["run_id"] != run.RunID || projected["task_id"] != taskID {
		t.Fatalf("ReferenceJSON() = %s", encoded)
	}

	validated, err := ValidateRun(repository, taskID, run.RunID)
	if err != nil {
		t.Fatalf("ValidateRun() after completion: %v", err)
	}
	original := readFileBytes(t, wantPath)
	record := decodeTestJSON(t, original)
	want := map[string]any{
		"schema_version":         json.Number("2"),
		"task_id":                taskID,
		"run_id":                 run.RunID,
		"evidence_sha256":        validated.evidenceSHA256,
		"verified_source_sha256": validated.sourceAfterSHA256,
		"current_source_sha256":  validated.sourceAfterSHA256,
		"final_result":           "pass",
		"completed_at":           completionTestTime.Format(completionTimestamp),
	}
	if !jsonEqual(record, want) {
		t.Fatalf("completion.json = %#v, want %#v", record, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(wantPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("completion mode = %o, want 600", info.Mode().Perm())
		}
	}

	if _, err := completeWithHooks(repository, taskID, run.RunID, completeHooks{
		now: func() time.Time { return completionTestTime.Add(24 * time.Hour) },
	}); err != nil {
		t.Fatalf("idempotent Complete(): %v", err)
	}
	if got := readFileBytes(t, wantPath); !bytes.Equal(got, original) {
		t.Fatalf("idempotent Complete changed bytes\nfirst: %s\nagain: %s", original, got)
	}

	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("source drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Complete(repository, taskID, run.RunID)
	assertCompletionExit(t, err, 9)
	if got := readFileBytes(t, wantPath); !bytes.Equal(got, original) {
		t.Fatal("source-mismatch rejection changed the existing completion record")
	}
	assertNoCompletionTemps(t, run.EvidencePath)
}

func TestCompletePreservesSemanticallyValidExistingV2Bytes(t *testing.T) {
	repository, taskID, run := passingCompletionRun(t)
	validated, err := ValidateRun(repository, taskID, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := renderCompletionRecord(validated, validated.sourceAfterSHA256, completionTestTime)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(decodeTestJSON(t, canonical))
	if err != nil {
		t.Fatal(err)
	}
	existing := append([]byte(" \n"), compact...)
	existing = append(existing, []byte("\n \t")...)
	path := filepath.Join(run.EvidencePath, "completion.json")
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Complete(repository, taskID, run.RunID); err != nil {
		t.Fatalf("Complete(): %v", err)
	}
	if got := readFileBytes(t, path); !bytes.Equal(got, existing) {
		t.Fatalf("valid existing v2 bytes changed\nwant: %q\ngot:  %q", existing, got)
	}
}

func TestCompleteDecisionPrecedence(t *testing.T) {
	tests := []struct {
		name             string
		checks           []map[string]any
		verifierRequired bool
		scopeViolation   bool
		driftAfterVerify bool
		wantExit         int
	}{
		{
			name:             "source mismatch before verifier",
			checks:           []map[string]any{verificationCheck("pass", "pass", true)},
			verifierRequired: true, driftAfterVerify: true, wantExit: 9,
		},
		{
			name:             "verifier before scope and timeout",
			checks:           []map[string]any{timedCompletionCheck(true)},
			verifierRequired: true, scopeViolation: true, wantExit: 7,
		},
		{
			name:           "scope before timeout",
			checks:         []map[string]any{timedCompletionCheck(true)},
			scopeViolation: true, wantExit: 4,
		},
		{
			name: "required timeout before other required failure",
			checks: []map[string]any{
				timedCompletionCheck(true),
				verificationCheck("failure", "fail", true),
			},
			wantExit: 6,
		},
		{
			name:     "required failure",
			checks:   []map[string]any{verificationCheck("failure", "fail", true)},
			wantExit: 5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := []any{"."}
			if test.scopeViolation {
				scope = []any{"README.md"}
			}
			repository, taskID := verificationRepositoryWithScope(t, test.checks, scope)
			if test.verifierRequired {
				setCompletionVerifierRequired(t, repository, taskID)
			}
			if test.scopeViolation {
				if err := os.WriteFile(filepath.Join(repository, "outside.txt"), []byte("outside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			run, err := Verify(repository, taskID)
			if err != nil {
				t.Fatalf("Verify(): %v", err)
			}
			if test.driftAfterVerify {
				if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("drift\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err = Complete(repository, taskID, run.RunID)
			assertCompletionExit(t, err, test.wantExit)
			if _, statErr := os.Lstat(filepath.Join(run.EvidencePath, "completion.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("completion record exists after rejected decision: %v", statErr)
			}
		})
	}
}

func TestCompleteAllowsOptionalFailureAndTimeout(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("optional failure", "fail", false),
		timedCompletionCheck(false),
	})
	run, err := Verify(repository, taskID)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	if _, err := Complete(repository, taskID, run.RunID); err != nil {
		t.Fatalf("Complete(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.EvidencePath, "completion.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteRejectsUnstableS0S1AndS2CollectionFailure(t *testing.T) {
	t.Run("saved S0 and S1 differ", func(t *testing.T) {
		repository, taskID := verificationRepository(t, []map[string]any{
			verificationCheck("mutate", "mutate", true),
		})
		run, err := Verify(repository, taskID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Complete(repository, taskID, run.RunID)
		assertCompletionExit(t, err, 9)
	})

	t.Run("S2 collection fails closed", func(t *testing.T) {
		repository, taskID, run := passingCompletionRun(t)
		t.Setenv("GIT_REPLACE_REF_BASE", "refs/replace")
		_, err := Complete(repository, taskID, run.RunID)
		assertCompletionExit(t, err, 3)
	})
}

func TestCompleteExistingRecordProblemsAreExitEightAndImmutable(t *testing.T) {
	tests := []struct {
		name  string
		bytes func(*testing.T, *ValidatedRun) []byte
		badS2 bool
	}{
		{
			name:  "legacy v1 precedes S2 collection failure",
			bytes: func(*testing.T, *ValidatedRun) []byte { return []byte("{\"schema_version\":1}\n") },
			badS2: true,
		},
		{
			name:  "corrupt JSON",
			bytes: func(*testing.T, *ValidatedRun) []byte { return []byte("{\n") },
		},
		{
			name: "different evidence digest",
			bytes: func(t *testing.T, run *ValidatedRun) []byte {
				contents := validCompletionBytes(t, run)
				return mutateCompletionJSON(t, contents, func(value map[string]any) {
					value["evidence_sha256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "different verified source digest",
			bytes: func(t *testing.T, run *ValidatedRun) []byte {
				return mutateCompletionJSON(t, validCompletionBytes(t, run), func(value map[string]any) {
					value["verified_source_sha256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "different current source digest",
			bytes: func(t *testing.T, run *ValidatedRun) []byte {
				return mutateCompletionJSON(t, validCompletionBytes(t, run), func(value map[string]any) {
					value["current_source_sha256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "duplicate key",
			bytes: func(t *testing.T, run *ValidatedRun) []byte {
				contents := bytes.TrimSpace(validCompletionBytes(t, run))
				contents = bytes.TrimSuffix(contents, []byte("}"))
				return append(contents, []byte(",\"task_id\":"+fmt.Sprintf("%q", run.taskID)+"}\n")...)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, taskID, verification := passingCompletionRun(t)
			validated, err := ValidateRun(repository, taskID, verification.RunID)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(verification.EvidencePath, "completion.json")
			original := test.bytes(t, validated)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if test.badS2 {
				t.Setenv("GIT_REPLACE_REF_BASE", "refs/replace")
			}
			_, err = Complete(repository, taskID, verification.RunID)
			assertCompletionExit(t, err, 8)
			if got := readFileBytes(t, path); !bytes.Equal(got, original) {
				t.Fatalf("rejected completion record changed\nwant: %q\ngot:  %q", original, got)
			}
			assertNoCompletionTemps(t, verification.EvidencePath)
		})
	}
}

func TestCompletionRecordParserEdgesAreEvidenceExitEight(t *testing.T) {
	repository, taskID, verification := passingCompletionRun(t)
	validated, err := ValidateRun(repository, taskID, verification.RunID)
	if err != nil {
		t.Fatal(err)
	}
	valid := validCompletionBytes(t, validated)
	tests := []struct {
		name     string
		contents []byte
	}{
		{name: "invalid UTF-8", contents: []byte{'{', 0xff, '}'}},
		{name: "4301 digit integer", contents: []byte(`{"schema_version":` + strings.Repeat("9", 4301) + `}`)},
		{name: "excessive depth", contents: []byte(`{"schema_version":2,"task_id":` + nestedArrayJSON(10001, `"x"`) + `}`)},
		{name: "extra key", contents: mutateCompletionJSON(t, valid, func(value map[string]any) { value["extra"] = true })},
		{name: "wrong type", contents: mutateCompletionJSON(t, valid, func(value map[string]any) { value["completed_at"] = 1 })},
		{name: "timestamp without fractions", contents: mutateCompletionJSON(t, valid, func(value map[string]any) { value["completed_at"] = "2026-08-16T07:08:09Z" })},
		{name: "timestamp with offset", contents: mutateCompletionJSON(t, valid, func(value map[string]any) { value["completed_at"] = "2026-08-16T07:08:09.123456+00:00" })},
		{name: "timestamp with milliseconds", contents: mutateCompletionJSON(t, valid, func(value map[string]any) { value["completed_at"] = "2026-08-16T07:08:09.123Z" })},
		{name: "timestamp with seven digits", contents: mutateCompletionJSON(t, valid, func(value map[string]any) { value["completed_at"] = "2026-08-16T07:08:09.1234567Z" })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCompletionRecord(test.contents, validated)
			assertCompletionExit(t, err, 8)
			if KindOf(err) != KindEvidence {
				t.Fatalf("KindOf(%v) = %v, want Evidence", err, KindOf(err))
			}
		})
	}
}

func TestCompleteRejectsSymlinkAndDirectoryDestinations(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		repository, taskID, run := passingCompletionRun(t)
		path := filepath.Join(run.EvidencePath, "completion.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := Complete(repository, taskID, run.RunID)
		assertCompletionExit(t, err, 8)
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("directory destination changed: %v, %v", info, statErr)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		repository, taskID, run := passingCompletionRun(t)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(run.EvidencePath, "completion.json")
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err := Complete(repository, taskID, run.RunID)
		assertCompletionExit(t, err, 8)
		if got := readFileBytes(t, outside); string(got) != "outside\n" {
			t.Fatalf("outside target changed: %q", got)
		}
	})
}

func TestCompleteWriterFaultsLeaveNoResidue(t *testing.T) {
	for _, point := range []string{"temp-created", "write", "sync-file", "sync-directory", "publish"} {
		t.Run(point, func(t *testing.T) {
			repository, taskID, run := passingCompletionRun(t)
			_, err := completeWithHooks(repository, taskID, run.RunID, completeHooks{
				writerFault: func(actual string) error {
					if actual == point {
						return errors.New("fault")
					}
					return nil
				},
			})
			assertCompletionExit(t, err, 8)
			if _, statErr := os.Lstat(filepath.Join(run.EvidencePath, "completion.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("completion destination exists: %v", statErr)
			}
			assertNoCompletionTemps(t, run.EvidencePath)
		})
	}
}

func TestConcurrentCompleteReusesNativeNoReplaceWinner(t *testing.T) {
	repository, taskID, run := passingCompletionRun(t)
	const count = 6
	var ready sync.WaitGroup
	ready.Add(count)
	release := make(chan struct{})
	hooks := completeHooks{
		writerFault: func(point string) error {
			if point == "publish" {
				ready.Done()
				<-release
			}
			return nil
		},
	}
	results := make([]*CompletionRun, count)
	errorsFound := make([]error, count)
	var finished sync.WaitGroup
	for index := range results {
		finished.Add(1)
		go func() {
			defer finished.Done()
			results[index], errorsFound[index] = completeWithHooks(repository, taskID, run.RunID, hooks)
		}()
	}
	allReady := make(chan struct{})
	go func() {
		ready.Wait()
		close(allReady)
	}()
	select {
	case <-allReady:
		close(release)
	case <-time.After(20 * time.Second):
		close(release)
		finished.Wait()
		t.Fatal("concurrent Complete calls did not all reach publication")
	}
	finished.Wait()
	for index, err := range errorsFound {
		if err != nil {
			t.Fatalf("Complete()[%d]: %v", index, err)
		}
		if results[index] == nil || results[index].CompletionPath != filepath.Join(run.EvidencePath, "completion.json") {
			t.Fatalf("Complete()[%d] result = %#v", index, results[index])
		}
	}
	contents := readFileBytes(t, filepath.Join(run.EvidencePath, "completion.json"))
	validated, err := ValidateRun(repository, taskID, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCompletionRecord(contents, validated); err != nil {
		t.Fatalf("winner completion record: %v", err)
	}
	assertNoCompletionTemps(t, run.EvidencePath)
}

func TestCompletePublicationCollisionValidatesWinner(t *testing.T) {
	for _, validWinner := range []bool{true, false} {
		name := "valid winner"
		if !validWinner {
			name = "invalid winner"
		}
		t.Run(name, func(t *testing.T) {
			repository, taskID, run := passingCompletionRun(t)
			validated, err := ValidateRun(repository, taskID, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			winner := validCompletionBytes(t, validated)
			if !validWinner {
				winner = []byte("{\"schema_version\":1}\n")
			}
			path := filepath.Join(run.EvidencePath, "completion.json")
			injected := false
			_, err = completeWithHooks(repository, taskID, run.RunID, completeHooks{
				writerFault: func(point string) error {
					if point != "publish" || injected {
						return nil
					}
					injected = true
					return os.WriteFile(path, winner, 0o600)
				},
			})
			if validWinner {
				if err != nil {
					t.Fatalf("Complete(): %v", err)
				}
			} else {
				assertCompletionExit(t, err, 8)
			}
			if got := readFileBytes(t, path); !bytes.Equal(got, winner) {
				t.Fatal("publication collision replaced winner bytes")
			}
			assertNoCompletionTemps(t, run.EvidencePath)
		})
	}
}

func passingCompletionRun(t *testing.T) (string, string, *VerificationRun) {
	t.Helper()
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	run, err := Verify(repository, taskID)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	return repository, taskID, run
}

func timedCompletionCheck(required bool) map[string]any {
	check := verificationCheck("timeout", "sleep", required)
	check["timeout_seconds"] = json.Number("1")
	return check
}

func setCompletionVerifierRequired(t *testing.T, repository, taskID string) {
	t.Helper()
	mutateTestJSON(t, filepath.Join(repository, ".seal", "tasks", taskID+".json"), func(task map[string]any) {
		task["verifier"].(map[string]any)["required"] = true
	})
}

func validCompletionBytes(t *testing.T, run *ValidatedRun) []byte {
	t.Helper()
	contents, err := renderCompletionRecord(run, run.sourceAfterSHA256, completionTestTime)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func mutateCompletionJSON(t *testing.T, contents []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	value := decodeTestJSON(t, contents)
	mutate(value)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func decodeTestJSON(t *testing.T, contents []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func assertCompletionExit(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("operation succeeded, want completion exit %d", want)
	}
	got, ok := CompletionExitCode(err)
	if !ok || got != want {
		t.Fatalf("CompletionExitCode(%v) = %d, %v; want %d, true", err, got, ok, want)
	}
}

func assertNoCompletionTemps(t *testing.T, runDirectory string) {
	t.Helper()
	entries, err := os.ReadDir(runDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".completion-") {
			t.Fatalf("completion staging residue remains: %s", entry.Name())
		}
	}
}
