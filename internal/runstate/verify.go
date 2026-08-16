package runstate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"time"

	"github.com/jgoneit/seal/internal/checkrun"
	"github.com/jgoneit/seal/internal/sourceobs"
)

// VerificationRun identifies one completely published Evidence Run.
type VerificationRun struct {
	TaskID       string
	RunID        string
	EvidencePath string
}

// ReferenceJSON returns the deterministic public verify result.
func (run VerificationRun) ReferenceJSON() ([]byte, error) {
	encoded, err := prettyCanonicalJSONMode(map[string]any{
		"evidence_path": run.EvidencePath,
		"run_id":        run.RunID,
	}, false, false)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// Verify validates one saved Task, runs its checks, records S0/S1 and Git
// evidence, and atomically publishes one manifest-complete Run directory.
// Check and Scope failures are recorded evidence and do not make Verify fail.
func Verify(cwd, taskID string) (*VerificationRun, error) {
	return verifyWithHooks(cwd, taskID, verifyHooks{})
}

type verifyHooks struct {
	writerFault    func(point string) error
	runIDGenerator func() (string, error)
}

func verifyWithHooks(cwd, taskID string, hooks verifyHooks) (result *VerificationRun, resultErr error) {
	if err := validateIdentity(taskID, "Task"); err != nil {
		return nil, err
	}
	repository, err := findRepositoryRoot(cwd)
	if err != nil {
		return nil, err
	}
	task, err := readSavedTask(repository, taskID)
	if err != nil {
		return nil, err
	}
	taskDefinition, err := parseTaskFacts(task, taskID, "saved Task snapshot")
	if err != nil {
		return nil, err
	}
	checks, err := verificationChecks(taskDefinition)
	if err != nil {
		return nil, err
	}
	taskBytes, err := renderEvidenceJSON(map[string]any(task))
	if err != nil {
		return nil, &RuntimeError{message: "Saved Task snapshot could not be rendered as UTF-8."}
	}

	started := time.Now()
	snapshotRequest := sourceobs.SnapshotRequest{
		CWD:      repository,
		Baseline: taskDefinition.baseline,
	}
	changesRequest := sourceobs.Request{
		CWD:      repository,
		Baseline: taskDefinition.baseline,
		Scope:    append([]string(nil), taskDefinition.scope...),
	}
	before, err := sourceobs.ObserveSnapshot(snapshotRequest)
	if err != nil {
		return nil, mapSourceObservationError(err)
	}

	writer, err := newEvidenceWriter(repository, taskID, hooks)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			if cleanupErr := writer.abort(); cleanupErr != nil {
				result = nil
				resultErr = &RepositoryError{message: "Verification failed and private Evidence staging cleanup also failed: " + errors.Join(resultErr, cleanupErr).Error()}
			}
		}
	}()

	if err := writer.write("task.json", taskBytes); err != nil {
		return nil, err
	}
	checkResults, err := checkrun.RunRooted(checks, repository, writer.staging)
	if err != nil {
		return nil, mapCheckRunError(err)
	}
	after, err := sourceobs.ObserveSnapshot(snapshotRequest)
	if err != nil {
		return nil, mapSourceObservationError(err)
	}
	observedChanges, err := sourceobs.ObserveChanges(changesRequest)
	if err != nil {
		return nil, mapSourceObservationError(err)
	}

	checksDocument, err := renderChecksDocument(checkResults)
	if err != nil {
		return nil, err
	}
	changes := observedChanges.Changes()
	sourceStable := before.SnapshotSHA256() == after.SnapshotSHA256()
	requiredPass := requiredChecksPassed(checkResults)
	scopePass := changes.ScopePassed()
	mechanicalResult := "fail"
	if sourceStable && requiredPass && scopePass {
		mechanicalResult = "pass"
	}

	evidenceFiles := evidenceFileList(checkResults)
	verificationDocument, err := renderVerificationDocument(verificationInput{
		TaskID:             taskID,
		RunID:              writer.runID,
		Baseline:           taskDefinition.baseline,
		Changes:            changes.ProductChanges,
		ScopeViolations:    changes.OutOfScopeChanges,
		ScopePass:          scopePass,
		RequiredChecksPass: requiredPass,
		SourceBeforeSHA256: before.SnapshotSHA256(),
		SourceAfterSHA256:  after.SnapshotSHA256(),
		SourceStable:       sourceStable,
		MechanicalResult:   mechanicalResult,
		EvidenceFiles:      evidenceFiles,
		Timestamp:          runTimestamp(time.Now()),
		Duration:           nonNegativeDuration(time.Since(started).Seconds()),
	})
	if err != nil {
		return nil, err
	}

	artifacts := []struct {
		path  string
		bytes []byte
	}{
		{"source-before-checks.json", before.SnapshotJSON()},
		{"source-after-checks.json", after.SnapshotJSON()},
		{"changed-files.json", observedChanges.ChangedFilesJSON()},
		{"diff.patch", observedChanges.DiffPatch()},
		{"checks.json", checksDocument},
		{"verification.json", verificationDocument},
	}
	for _, artifact := range artifacts {
		if err := writer.write(artifact.path, artifact.bytes); err != nil {
			return nil, err
		}
	}
	if err := writer.writeManifest(evidenceFiles); err != nil {
		return nil, err
	}
	if err := writer.inject("self-validate"); err != nil {
		return nil, err
	}
	if err := writer.validateStagingBinding(); err != nil {
		return nil, err
	}
	if _, err := validateRunAt(repository, task, taskID, writer.runID, writer.stagingPath()); err != nil {
		return nil, &RepositoryError{message: "Generated Evidence failed its own integrity validation: " + err.Error()}
	}
	if err := writer.validateStagingBinding(); err != nil {
		return nil, err
	}
	if err := writer.commit(); err != nil {
		return nil, err
	}
	committed = true
	return &VerificationRun{
		TaskID:       taskID,
		RunID:        writer.runID,
		EvidencePath: filepath.Join(repository, ".seal", "evidence", taskID, writer.runID),
	}, nil
}

func verificationChecks(task taskFacts) ([]checkrun.Definition, error) {
	checks := make([]checkrun.Definition, len(task.checks))
	for index, saved := range task.checks {
		name := saved.raw["name"].(string)
		rawArgv := saved.raw["argv"].([]any)
		argv := make([]string, len(rawArgv))
		for position, value := range rawArgv {
			argv[position] = value.(string)
		}
		timeout, ok := new(big.Int).SetString(fmt.Sprint(saved.timeout), 10)
		if !ok || timeout.Sign() <= 0 {
			return nil, &IdentityError{message: fmt.Sprintf(
				"saved Task snapshot checks[%d].timeout_seconds must be a positive integer.",
				index,
			)}
		}
		checks[index] = checkrun.Definition{
			Name:           name,
			Argv:           argv,
			Required:       saved.required,
			TimeoutSeconds: timeout,
		}
	}
	return checks, nil
}

func renderChecksDocument(results []checkrun.Result) ([]byte, error) {
	records := make([]any, len(results))
	for index, result := range results {
		records[index] = checkResultDocument(result)
	}
	return renderEvidenceJSON(map[string]any{
		"schema_version": json.Number("1"),
		"checks":         records,
	})
}

func checkResultDocument(result checkrun.Result) map[string]any {
	var exitCode any
	if result.ExitCode != nil {
		exitCode = *result.ExitCode
	}
	return map[string]any{
		"name":              result.Name,
		"argv":              stringSliceValues(result.Argv),
		"cwd":               result.CWD,
		"started_at":        result.StartedAt,
		"finished_at":       result.FinishedAt,
		"duration_seconds":  pythonFloat(result.DurationSeconds),
		"effective_timeout": json.Number(result.EffectiveTimeout.String()),
		"exit_code":         exitCode,
		"timed_out":         result.TimedOut,
		"stdout_path":       result.StdoutPath,
		"stderr_path":       result.StderrPath,
		"required":          result.Required,
		"passed":            result.Passed,
	}
}

type verificationInput struct {
	TaskID             string
	RunID              string
	Baseline           string
	Changes            []sourceobs.Change
	ScopeViolations    []sourceobs.Change
	ScopePass          bool
	RequiredChecksPass bool
	SourceBeforeSHA256 string
	SourceAfterSHA256  string
	SourceStable       bool
	MechanicalResult   string
	EvidenceFiles      []string
	Timestamp          string
	Duration           float64
}

func renderVerificationDocument(input verificationInput) ([]byte, error) {
	return renderEvidenceJSON(map[string]any{
		"schema_version":                 json.Number("2"),
		"task_id":                        input.TaskID,
		"run_id":                         input.RunID,
		"baseline":                       input.Baseline,
		"changed_files":                  changeDocuments(input.Changes),
		"scope_pass":                     input.ScopePass,
		"scope_violations":               changeDocuments(input.ScopeViolations),
		"required_checks_pass":           input.RequiredChecksPass,
		"source_snapshot_schema_version": json.Number("1"),
		"source_before_checks_sha256":    input.SourceBeforeSHA256,
		"source_after_checks_sha256":     input.SourceAfterSHA256,
		"source_stable_during_checks":    input.SourceStable,
		"mechanical_result":              input.MechanicalResult,
		"evidence_files":                 stringSliceValues(input.EvidenceFiles),
		"timestamp":                      input.Timestamp,
		"duration":                       pythonFloat(input.Duration),
	})
}

func changeDocuments(changes []sourceobs.Change) []any {
	documents := make([]any, len(changes))
	for index, change := range changes {
		var previousPath any
		if change.PreviousPath != nil {
			previousPath = *change.PreviousPath
		}
		var oldMode any
		if change.OldMode != nil {
			oldMode = *change.OldMode
		}
		var newMode any
		if change.NewMode != nil {
			newMode = *change.NewMode
		}
		documents[index] = map[string]any{
			"source":        change.Source,
			"status":        change.Status,
			"path":          change.Path,
			"previous_path": previousPath,
			"old_mode":      oldMode,
			"new_mode":      newMode,
			"mode_changed":  change.ModeChanged,
			"is_binary":     change.Binary,
			"in_scope":      change.InScope,
		}
	}
	return documents
}

func evidenceFileList(results []checkrun.Result) []string {
	files := []string{
		"task.json",
		"source-before-checks.json",
		"source-after-checks.json",
		"changed-files.json",
		"diff.patch",
		"checks.json",
	}
	for _, result := range results {
		files = append(files, result.StdoutPath, result.StderrPath)
	}
	return append(files, "verification.json")
}

func requiredChecksPassed(results []checkrun.Result) bool {
	for _, result := range results {
		if result.Required && !result.Passed {
			return false
		}
	}
	return true
}

func renderEvidenceJSON(value any) ([]byte, error) {
	encoded, err := prettyCanonicalJSONMode(value, false, false)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func stringSliceValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func runTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func nonNegativeDuration(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func mapCheckRunError(err error) error {
	var definition *checkrun.DefinitionError
	if errors.As(err, &definition) {
		return &IdentityError{message: definition.Error()}
	}
	return &RepositoryError{message: err.Error()}
}

func mapSourceObservationError(err error) error {
	kind, ok := sourceobs.KindOf(err)
	if ok && kind == sourceobs.InvalidRequest {
		return &IdentityError{message: err.Error()}
	}
	return &RepositoryError{message: err.Error()}
}

func generateRunID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return hex.EncodeToString(value), nil
}
