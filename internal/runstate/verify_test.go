package runstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVerifyPublishesManifestValidRunForPassingAndFailedChecks(t *testing.T) {
	tests := []struct {
		name             string
		mode             string
		required         bool
		wantMechanical   string
		wantRequiredPass bool
		wantExitCode     string
	}{
		{
			name:             "passing required check",
			mode:             "pass",
			required:         true,
			wantMechanical:   "pass",
			wantRequiredPass: true,
			wantExitCode:     "0",
		},
		{
			name:             "failed required check is still a recorded Run",
			mode:             "fail",
			required:         true,
			wantMechanical:   "fail",
			wantRequiredPass: false,
			wantExitCode:     "17",
		},
		{
			name:             "failed optional check does not block mechanical pass",
			mode:             "fail",
			required:         false,
			wantMechanical:   "pass",
			wantRequiredPass: true,
			wantExitCode:     "17",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, taskID := verificationRepository(t, []map[string]any{
				verificationCheck("contract", test.mode, test.required),
			})

			run, err := Verify(repository, taskID)
			if err != nil {
				t.Fatalf("Verify(): %v", err)
			}
			if !validGeneratedRunID(run.RunID) {
				t.Fatalf("run id = %q, want 32 lowercase hex", run.RunID)
			}
			wantPath := filepath.Join(repository, ".seal", "evidence", taskID, run.RunID)
			if run.EvidencePath != wantPath {
				t.Fatalf("evidence path = %q, want %q", run.EvidencePath, wantPath)
			}

			validated, err := ValidateRun(repository, taskID, run.RunID)
			if err != nil {
				t.Fatalf("ValidateRun(): %v", err)
			}
			summary := validated.Summary()
			if summary.MechanicalResult != test.wantMechanical {
				t.Fatalf("mechanical result = %q, want %q", summary.MechanicalResult, test.wantMechanical)
			}
			if summary.RequiredChecksPass != test.wantRequiredPass {
				t.Fatalf("required checks pass = %v, want %v", summary.RequiredChecksPass, test.wantRequiredPass)
			}
			if got := summary.Checks[0].ExitCode.String(); got != test.wantExitCode {
				t.Fatalf("exit code = %s, want %s", got, test.wantExitCode)
			}
			if got := readVerificationLog(t, run.EvidencePath, summary.Checks[0].Name, "stdout"); !bytes.Equal(got, []byte{'r', 'a', 'w', 0x00, 0xff, '\n'}) {
				t.Fatalf("stdout log bytes = % x", got)
			}
			assertNoStagingDirectories(t, filepath.Dir(run.EvidencePath))
		})
	}
}

func TestVerifyRecordsSourceInstabilityWithoutReturningFailure(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("mutate", "mutate", true),
	})
	run, err := Verify(repository, taskID)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	validated, err := ValidateRun(repository, taskID, run.RunID)
	if err != nil {
		t.Fatalf("ValidateRun(): %v", err)
	}
	summary := validated.Summary()
	if summary.SourceStableDuringChecks || summary.MechanicalResult != "fail" {
		t.Fatalf("summary = %#v, want unstable mechanical failure", summary)
	}
}

func TestVerifyRecordsTimeoutLaunchFailureAndContinues(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-check")
	timeout := verificationCheck("timeout", "sleep", false)
	timeout["timeout_seconds"] = json.Number("1")
	repository, taskID := verificationRepository(t, []map[string]any{
		{
			"name":     "launch failure",
			"argv":     []any{missing},
			"required": true,
		},
		timeout,
		verificationCheck("last pass", "pass", true),
	})

	run, err := Verify(repository, taskID)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	validated, err := ValidateRun(repository, taskID, run.RunID)
	if err != nil {
		t.Fatalf("ValidateRun(): %v", err)
	}
	summary := validated.Summary()
	if summary.MechanicalResult != "fail" || summary.RequiredChecksPass {
		t.Fatalf("summary = %#v, want required-check failure", summary)
	}
	if got := []string{summary.Checks[0].Name, summary.Checks[1].Name, summary.Checks[2].Name}; !reflect.DeepEqual(got, []string{"launch failure", "timeout", "last pass"}) {
		t.Fatalf("check order = %v", got)
	}
	if summary.Checks[0].ExitCode != nil || summary.Checks[0].TimedOut || summary.Checks[0].Passed {
		t.Fatalf("launch failure = %#v", summary.Checks[0])
	}
	if !summary.Checks[1].TimedOut || summary.Checks[1].Passed {
		t.Fatalf("timeout = %#v", summary.Checks[1])
	}
	if summary.Checks[2].ExitCode == nil || summary.Checks[2].ExitCode.String() != "0" || !summary.Checks[2].Passed {
		t.Fatalf("last pass = %#v", summary.Checks[2])
	}
}

func TestVerifyAcceptsTheBasicProfileTimeoutMaximum(t *testing.T) {
	check := verificationCheck("maximum", "pass", true)
	check["timeout_seconds"] = json.Number("300")
	repository, taskID := verificationRepository(t, []map[string]any{check})

	run, err := Verify(repository, taskID)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	if _, err := ValidateRun(repository, taskID, run.RunID); err != nil {
		t.Fatalf("ValidateRun(): %v", err)
	}
}

func TestVerifyRejectsSavedTimeoutAboveBasicProfileMaximumBeforeS0(t *testing.T) {
	tests := []struct {
		name    string
		timeout json.Number
	}{
		{name: "one second above", timeout: json.Number("301")},
		{name: "arbitrary precision", timeout: json.Number("999999999999999999999999999999999999999999")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := verificationCheck("must not run", "mutate", true)
			check["timeout_seconds"] = test.timeout
			repository, taskID := verificationRepository(t, []map[string]any{check})
			mutateTestJSON(t, filepath.Join(repository, ".seal", "tasks", taskID+".json"), func(task map[string]any) {
				task["baseline"] = strings.Repeat("f", 40)
			})

			_, err := Verify(repository, taskID)
			want := "saved Task snapshot checks[0].timeout_seconds must be at most 300 seconds."
			if err == nil || KindOf(err) != KindInvalidInput || err.Error() != want {
				t.Fatalf("Verify() error = %v, kind = %v; want %q", err, KindOf(err), want)
			}
			if contents, readErr := os.ReadFile(filepath.Join(repository, "README.md")); readErr != nil || string(contents) != "baseline\n" {
				t.Fatalf("rejected check ran: README = %q, error = %v", contents, readErr)
			}
			assertNoPublishedOrStagingRun(t, filepath.Join(repository, ".seal", "evidence", taskID))
		})
	}
}

func TestVerifyWallClockBudgetIsSharedAcrossChecks(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "completed-checks")
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationDelayCheck("first", 1200, marker, "first"),
		verificationDelayCheck("second", 1200, marker, "second"),
	})

	prepared, err := prepareVerification(repository, taskID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = verifyPreparedContext(ctx, prepared, verifyHooks{})
	if err == nil || KindOf(err) != KindRepository || err.Error() != verificationDeadlineMessage {
		t.Fatalf("verifyPreparedContext() error = %v, kind = %v; want wall-clock RepositoryError", err, KindOf(err))
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("read marker: %v", readErr)
	}
	if string(contents) != "first\n" {
		t.Fatalf("completed checks = %q, want only first check", contents)
	}
	assertNoPublishedOrStagingRun(t, filepath.Join(repository, ".seal", "evidence", taskID))
}

func TestVerifyMapsStreamLimitToRepositoryFailureWithoutPublishing(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationOutputCheck("overflow", "stdout-bytes", 8*1024*1024+1),
	})

	_, err := Verify(repository, taskID)
	want := "checks[0].stdout exceeded the 8388608-byte safety limit."
	if err == nil || KindOf(err) != KindRepository || err.Error() != want {
		t.Fatalf("Verify() error = %v, kind = %v; want %q", err, KindOf(err), want)
	}
	assertNoPublishedOrStagingRun(t, filepath.Join(repository, ".seal", "evidence", taskID))
}

func TestVerifyRecordsScopeViolationAsValidFailedRun(t *testing.T) {
	repository, taskID := verificationRepositoryWithScope(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	}, []any{"README.md"})
	if err := os.WriteFile(filepath.Join(repository, "outside.txt"), []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := Verify(repository, taskID)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	validated, err := ValidateRun(repository, taskID, run.RunID)
	if err != nil {
		t.Fatalf("ValidateRun(): %v", err)
	}
	summary := validated.Summary()
	if summary.ScopePass || summary.MechanicalResult != "fail" || len(summary.ScopeViolations) != 1 {
		t.Fatalf("summary = %#v, want one Scope violation", summary)
	}
	if summary.ScopeViolations[0].Path != "outside.txt" {
		t.Fatalf("violation = %#v", summary.ScopeViolations[0])
	}
}

func TestVerifyCleansPrivateStagingAfterInjectedPrePublicationFailures(t *testing.T) {
	points := []string{
		"staging-created",
		"write:task.json",
		"sync-file:task.json",
		"write:verification.json",
		"manifest",
		"sync-manifest-file:task.json",
		"self-validate",
		"sync-staging-directory",
		"sync-publication-parent",
		"publish",
	}
	for _, point := range points {
		t.Run(strings.ReplaceAll(point, ":", "_"), func(t *testing.T) {
			repository, taskID := verificationRepository(t, []map[string]any{
				verificationCheck("pass", "pass", true),
			})
			_, err := verifyWithHooks(repository, taskID, verifyHooks{
				writerFault: func(actual string) error {
					if actual == point {
						return errors.New("fault")
					}
					return nil
				},
			})
			if err == nil || KindOf(err) != KindRepository {
				t.Fatalf("verifyWithHooks() error = %v, kind = %v; want repository failure", err, KindOf(err))
			}
			parent := filepath.Join(repository, ".seal", "evidence", taskID)
			assertNoPublishedOrStagingRun(t, parent)
		})
	}
}

func TestVerifyRejectsSymlinkedEvidenceWriterPathWithoutEscape(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repository, ".seal", "evidence")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := Verify(repository, taskID)
	if err == nil || KindOf(err) != KindRepository {
		t.Fatalf("Verify() error = %v, kind = %v; want repository failure", err, KindOf(err))
	}
	contents, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(contents) != "unchanged\n" {
		t.Fatalf("outside sentinel changed: contents %q, error %v", contents, readErr)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("outside directory changed: entries %v, error %v", entries, readErr)
	}
}

func TestVerifyRejectsStagingReplacementBeforeReopenWithoutDeletingIt(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	runID := strings.Repeat("d", 32)
	parent := filepath.Join(repository, ".seal", "evidence", taskID)
	staging := filepath.Join(parent, ".tmp-"+runID)
	final := filepath.Join(parent, runID)
	replacement := filepath.Join(repository, "attacker-replacement")
	quarantine := filepath.Join(repository, "created-staging-quarantine")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := verifyWithHooks(repository, taskID, verifyHooks{
		runIDGenerator: func() (string, error) { return runID, nil },
		writerFault: func(point string) error {
			if point != "staging-created" {
				return nil
			}
			if err := os.Rename(staging, quarantine); err != nil {
				return err
			}
			return os.Rename(replacement, staging)
		},
	})
	if err == nil || KindOf(err) != KindRepository || !strings.Contains(err.Error(), "identities do not match") {
		t.Fatalf("verifyWithHooks() error = %v, kind = %v; want staging identity failure", err, KindOf(err))
	}
	if _, statErr := os.Lstat(final); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("published Run exists after staging replacement: %v", statErr)
	}
	contents, readErr := os.ReadFile(filepath.Join(staging, "sentinel"))
	if readErr != nil || string(contents) != "unchanged\n" {
		t.Fatalf("replacement sentinel changed: contents %q, error %v", contents, readErr)
	}
	replacementEntries, readErr := os.ReadDir(staging)
	if readErr != nil || len(replacementEntries) != 1 || replacementEntries[0].Name() != "sentinel" {
		t.Fatalf("replacement directory changed: entries %v, error %v", replacementEntries, readErr)
	}
	createdEntries, readErr := os.ReadDir(quarantine)
	if readErr != nil || len(createdEntries) != 0 {
		t.Fatalf("created staging directory received artifacts: entries %v, error %v", createdEntries, readErr)
	}
}

func TestVerifyPublishesConcurrentIndependentRuns(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	const count = 4
	runs := make([]*VerificationRun, count)
	errorsFound := make([]error, count)
	var wait sync.WaitGroup
	for index := range runs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runs[index], errorsFound[index] = Verify(repository, taskID)
		}()
	}
	wait.Wait()
	seen := make(map[string]struct{}, count)
	for index, err := range errorsFound {
		if err != nil {
			t.Fatalf("Verify()[%d]: %v", index, err)
		}
		if _, duplicate := seen[runs[index].RunID]; duplicate {
			t.Fatalf("duplicate run id %q", runs[index].RunID)
		}
		seen[runs[index].RunID] = struct{}{}
		if _, err := ValidateRun(repository, taskID, runs[index].RunID); err != nil {
			t.Fatalf("ValidateRun()[%d]: %v", index, err)
		}
	}
	assertNoStagingDirectories(t, filepath.Join(repository, ".seal", "evidence", taskID))
}

func TestVerifyPublicationNeverReplacesConcurrentRunWinner(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	runID := strings.Repeat("b", 32)
	winner := filepath.Join(repository, ".seal", "evidence", taskID, runID)
	_, err := verifyWithHooks(repository, taskID, verifyHooks{
		runIDGenerator: func() (string, error) { return runID, nil },
		writerFault: func(point string) error {
			if point != "publish" {
				return nil
			}
			if err := os.Mkdir(winner, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(winner, "sentinel"), []byte("winner\n"), 0o600)
		},
	})
	if err == nil || KindOf(err) != KindRepository {
		t.Fatalf("verifyWithHooks() error = %v, kind = %v; want publication failure", err, KindOf(err))
	}
	contents, readErr := os.ReadFile(filepath.Join(winner, "sentinel"))
	if readErr != nil || string(contents) != "winner\n" {
		t.Fatalf("winner changed: contents %q, error %v", contents, readErr)
	}
	assertNoStagingDirectories(t, filepath.Dir(winner))
}

func TestEvidenceCommitWinsWhenContextIsCanceledAfterNativePublication(t *testing.T) {
	repository := t.TempDir()
	runID := strings.Repeat("c", 32)
	finalDirectory := filepath.Join(repository, ".seal", "evidence", "TASK-PUBLISH-DEADLINE", runID)
	ctx := cancelWhenPathExistsContext{Context: context.Background(), path: finalDirectory}
	writer, err := newEvidenceWriter(repository, "TASK-PUBLISH-DEADLINE", verifyHooks{
		runIDGenerator: func() (string, error) { return runID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	writer.ctx = ctx
	committed := false
	defer func() {
		if !committed {
			_ = writer.abort()
		}
	}()
	if err := writer.write("sentinel", []byte("published\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.commit(); err != nil {
		t.Fatalf("commit after native publication cancellation: %v", err)
	}
	committed = true
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("publication-aware context did not observe the committed directory")
	}
	path := filepath.Join(finalDirectory, "sentinel")
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "published\n" {
		t.Fatalf("published sentinel = %q, error = %v", contents, err)
	}
}

type cancelWhenPathExistsContext struct {
	context.Context
	path string
}

func (ctx cancelWhenPathExistsContext) Err() error {
	if _, err := os.Stat(ctx.path); err == nil {
		return context.Canceled
	}
	return ctx.Context.Err()
}

func TestVerifySurfacesCleanupFailureAndStillAttemptsRemoval(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	_, err := verifyWithHooks(repository, taskID, verifyHooks{
		writerFault: func(point string) error {
			if point == "write:task.json" || point == "abort-remove" {
				return errors.New("fault")
			}
			return nil
		},
	})
	if err == nil || KindOf(err) != KindRepository || !strings.Contains(err.Error(), "cleanup also failed") {
		t.Fatalf("verifyWithHooks() error = %v, kind = %v", err, KindOf(err))
	}
	assertNoPublishedOrStagingRun(t, filepath.Join(repository, ".seal", "evidence", taskID))
}

func TestVerifyExhaustsRunIDCollisionsWithoutOverwriting(t *testing.T) {
	repository, taskID := verificationRepository(t, []map[string]any{
		verificationCheck("pass", "pass", true),
	})
	runID := strings.Repeat("a", 32)
	existing := filepath.Join(repository, ".seal", "evidence", taskID, runID)
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := verifyWithHooks(repository, taskID, verifyHooks{
		runIDGenerator: func() (string, error) { return runID, nil },
	})
	if err == nil || KindOf(err) != KindRepository {
		t.Fatalf("verifyWithHooks() error = %v, kind = %v; want allocation exhaustion", err, KindOf(err))
	}
	contents, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(contents) != "unchanged\n" {
		t.Fatalf("existing Run changed: contents %q, error %v", contents, readErr)
	}
	assertNoStagingDirectories(t, filepath.Dir(existing))
}

func TestVerifyManagedCheckHelper(t *testing.T) {
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
	mode := os.Args[separator+1]
	switch mode {
	case "pass":
		_, _ = os.Stdout.Write([]byte{'r', 'a', 'w', 0x00, 0xff, '\n'})
		os.Exit(0)
	case "fail":
		_, _ = os.Stdout.Write([]byte{'r', 'a', 'w', 0x00, 0xff, '\n'})
		os.Exit(17)
	case "mutate":
		_, _ = os.Stdout.Write([]byte{'r', 'a', 'w', 0x00, 0xff, '\n'})
		if separator+2 >= len(os.Args) {
			os.Exit(96)
		}
		if err := os.WriteFile(os.Args[separator+2], []byte("changed\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(96)
		}
		os.Exit(0)
	case "sleep":
		_, _ = os.Stdout.Write([]byte{'r', 'a', 'w', 0x00, 0xff, '\n'})
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "delay-marker":
		if separator+4 >= len(os.Args) {
			os.Exit(96)
		}
		delay, err := strconv.Atoi(os.Args[separator+2])
		if err != nil {
			os.Exit(96)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
		file, err := os.OpenFile(os.Args[separator+3], os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			os.Exit(96)
		}
		_, writeErr := fmt.Fprintln(file, os.Args[separator+4])
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			os.Exit(96)
		}
		os.Exit(0)
	case "stdout-bytes", "stderr-bytes":
		if separator+2 >= len(os.Args) {
			os.Exit(96)
		}
		count, err := strconv.Atoi(os.Args[separator+2])
		if err != nil || count < 0 {
			os.Exit(96)
		}
		writer := os.Stdout
		if mode == "stderr-bytes" {
			writer = os.Stderr
		}
		chunk := make([]byte, 64*1024)
		for count > 0 {
			length := len(chunk)
			if count < length {
				length = count
			}
			written, err := writer.Write(chunk[:length])
			if err != nil {
				os.Exit(96)
			}
			count -= written
		}
		os.Exit(0)
	default:
		os.Exit(95)
	}
}

func verificationDelayCheck(name string, milliseconds int, marker, value string) map[string]any {
	return map[string]any{
		"name": name,
		"argv": []any{
			os.Args[0], "-test.run=^TestVerifyManagedCheckHelper$", "--",
			"delay-marker", strconv.Itoa(milliseconds), marker, value,
		},
		"required":        true,
		"timeout_seconds": json.Number("5"),
	}
}

func verificationOutputCheck(name, mode string, bytes int) map[string]any {
	return map[string]any{
		"name": name,
		"argv": []any{
			os.Args[0], "-test.run=^TestVerifyManagedCheckHelper$", "--", mode, strconv.Itoa(bytes),
		},
		"required":        true,
		"timeout_seconds": json.Number("30"),
	}
}

func verificationRepository(t *testing.T, checks []map[string]any) (string, string) {
	return verificationRepositoryWithScope(t, checks, []any{"."})
}

func verificationRepositoryWithScope(t *testing.T, checks []map[string]any, scope []any) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	verificationGit(t, repository, "init", "--quiet")
	verificationGit(t, repository, "config", "maintenance.auto", "false")
	verificationGit(t, repository, "config", "maintenance.autoDetach", "false")
	verificationGit(t, repository, "config", "gc.auto", "0")
	readme := filepath.Join(repository, "README.md")
	if err := os.WriteFile(readme, []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verificationGit(t, repository, "add", "README.md")
	verificationGit(t, repository,
		"-c", "user.name=Seal Verify Test",
		"-c", "user.email=seal-verify@example.invalid",
		"commit", "--quiet", "-m", "baseline",
	)
	baseline := strings.TrimSpace(verificationGit(t, repository, "rev-parse", "HEAD"))
	taskID := "TASK-VERIFY-001"
	values := make([]any, len(checks))
	for index, check := range checks {
		values[index] = check
	}
	task := map[string]any{
		"schema_version": json.Number("1"),
		"id":             taskID,
		"type":           "test",
		"objective":      "Record deterministic verification Evidence.",
		"scope":          scope,
		"checks":         values,
		"risk":           "low",
		"verifier":       map[string]any{"required": false},
		"baseline":       baseline,
	}
	encoded, err := renderEvidenceJSON(task)
	if err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(repository, ".seal", "tasks", taskID+".json")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, taskID
}

func verificationCheck(name, mode string, required bool) map[string]any {
	arguments := []any{os.Args[0], "-test.run=^TestVerifyManagedCheckHelper$", "--", mode}
	if mode == "mutate" {
		arguments = append(arguments, "README.md")
	}
	return map[string]any{
		"name":     name,
		"argv":     arguments,
		"required": required,
	}
}

func verificationGit(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func readVerificationLog(t *testing.T, runDirectory, checkName, extension string) []byte {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runDirectory, "checks"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "."+extension) {
			contents, err := os.ReadFile(filepath.Join(runDirectory, "checks", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			return contents
		}
	}
	t.Fatalf("missing %s log for %q", extension, checkName)
	return nil
}

func assertNoPublishedOrStagingRun(t *testing.T, parent string) {
	t.Helper()
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
		sort.Strings(names)
		t.Fatalf("unexpected Evidence residue: %v", names)
	}
}

func assertNoStagingDirectories(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("staging residue remains: %s", entry.Name())
		}
	}
}

func TestVerificationRunReferenceJSON(t *testing.T) {
	run := VerificationRun{RunID: strings.Repeat("a", 32), EvidencePath: "/repo/.seal/evidence/TASK/run"}
	got, err := run.ReferenceJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("{\n  \"evidence_path\": \"/repo/.seal/evidence/TASK/run\",\n  \"run_id\": \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n}\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReferenceJSON() = %q, want %q", got, want)
	}
}
