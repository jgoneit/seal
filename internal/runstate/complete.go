package runstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/jgoneit/seal/internal/sourceobs"
)

const (
	completionSchemaVersion = 2
	completionTimestamp     = "2006-01-02T15:04:05.000000Z"
)

// CompletionRun identifies one immutable completion record. It exposes only
// the legacy success projection needed by the later CLI boundary.
type CompletionRun struct {
	TaskID         string
	RunID          string
	CompletionPath string
}

// ReferenceJSON returns the deterministic legacy completion success object.
func (run CompletionRun) ReferenceJSON() ([]byte, error) {
	encoded, err := prettyCanonicalJSONMode(map[string]any{
		"completion_path": run.CompletionPath,
		"run_id":          run.RunID,
		"task_id":         run.TaskID,
	}, false, false)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

type completionPolicyError struct {
	exitCode int
	message  string
}

func (e *completionPolicyError) Error() string { return e.message }

// CompletionExitCode maps a handled Complete error to its stable CLI exit.
// It is deliberately narrower than exporting completion policy internals.
func CompletionExitCode(err error) (int, bool) {
	var policy *completionPolicyError
	if errors.As(err, &policy) {
		return policy.exitCode, true
	}
	switch KindOf(err) {
	case KindRuntime:
		return 1, true
	case KindInvalidInput:
		return 2, true
	case KindRepository:
		return 3, true
	case KindEvidence:
		return 8, true
	default:
		return 0, false
	}
}

// Complete evaluates one exact, validated Run against the current source and
// records immutable Basic-profile acceptance. It never reruns checks, reads a
// Verdict, repairs Evidence, or selects another Run.
func Complete(cwd, taskID, runID string) (*CompletionRun, error) {
	return completeWithHooks(cwd, taskID, runID, completeHooks{})
}

type completeHooks struct {
	observeSnapshot   func(sourceobs.SnapshotRequest) (sourceobs.SnapshotResult, error)
	now               func() time.Time
	writerFault       func(string) error
	tempNameGenerator func() (string, error)
}

func completeWithHooks(cwd, taskID, runID string, hooks completeHooks) (*CompletionRun, error) {
	validated, err := ValidateRun(cwd, taskID, runID)
	if err != nil {
		return nil, err
	}
	store, err := openCompletionStore(validated, hooks)
	if err != nil {
		return nil, err
	}
	defer store.close()

	existing, err := store.readExisting(validated)
	if err != nil {
		return nil, err
	}

	observe := hooks.observeSnapshot
	if observe == nil {
		observe = sourceobs.ObserveSnapshot
	}
	current, err := observe(sourceobs.SnapshotRequest{
		CWD:      validated.repository,
		Baseline: validated.baseline,
	})
	if err != nil {
		return nil, mapSourceObservationError(err)
	}
	currentSHA256 := current.SnapshotSHA256()
	if validated.sourceBeforeSHA256 != validated.sourceAfterSHA256 ||
		validated.sourceAfterSHA256 != currentSHA256 {
		return nil, &completionPolicyError{
			exitCode: 9,
			message:  "Completion rejected because S0, S1, and current S2 source identities do not match.",
		}
	}
	if validated.verifierRequired {
		return nil, &completionPolicyError{
			exitCode: 7,
			message:  "Completion rejected because verifier.required=true is unsupported by the Go v1 Basic Profile.",
		}
	}
	if !validated.scopePass {
		return nil, &completionPolicyError{
			exitCode: 4,
			message:  "Completion rejected because saved evidence contains a Scope violation.",
		}
	}
	for _, check := range validated.checks {
		required, _ := check.Required.(bool)
		if required && check.TimedOut {
			return nil, &completionPolicyError{
				exitCode: 6,
				message:  "Completion rejected because a required check timed out.",
			}
		}
	}
	if !validated.requiredChecksPass {
		return nil, &completionPolicyError{
			exitCode: 5,
			message:  "Completion rejected because a required check did not pass.",
		}
	}

	result := &CompletionRun{
		TaskID:         taskID,
		RunID:          runID,
		CompletionPath: filepath.Join(validated.runDirectory, "completion.json"),
	}
	if existing != nil {
		return result, nil
	}

	now := hooks.now
	if now == nil {
		now = time.Now
	}
	record, err := renderCompletionRecord(validated, currentSHA256, now())
	if err != nil {
		return nil, &EvidenceError{message: "Could not render completion.json."}
	}
	if _, err := store.publish(validated, record); err != nil {
		return nil, err
	}
	return result, nil
}

func renderCompletionRecord(run *ValidatedRun, currentSHA256 string, completedAt time.Time) ([]byte, error) {
	return renderEvidenceJSON(map[string]any{
		"schema_version":         json.Number("2"),
		"task_id":                run.taskID,
		"run_id":                 run.runID,
		"evidence_sha256":        run.evidenceSHA256,
		"verified_source_sha256": run.sourceAfterSHA256,
		"current_source_sha256":  currentSHA256,
		"final_result":           "pass",
		"completed_at":           completedAt.UTC().Format(completionTimestamp),
	})
}

func validateCompletionRecord(contents []byte, run *ValidatedRun) error {
	document, err := decodeCompletionObject(contents)
	if err != nil {
		return &EvidenceError{message: "completion.json is not a valid exact-v2 completion record."}
	}
	if !exactKeys(document,
		"schema_version", "task_id", "run_id", "evidence_sha256",
		"verified_source_sha256", "current_source_sha256", "final_result", "completed_at",
	) {
		return &EvidenceError{message: "completion.json has missing, duplicate, or unexpected field(s)."}
	}
	if !integerEquals(document["schema_version"], completionSchemaVersion) {
		return &EvidenceError{message: "completion.json has an unsupported schema_version."}
	}
	for _, field := range []string{
		"task_id", "run_id", "evidence_sha256", "verified_source_sha256",
		"current_source_sha256", "final_result", "completed_at",
	} {
		if _, ok := document[field].(string); !ok {
			return &EvidenceError{message: "completion.json " + field + " must be a string."}
		}
	}
	if document["task_id"] != run.taskID || document["run_id"] != run.runID {
		return &EvidenceError{message: "completion.json identity does not match the requested Task and Run."}
	}
	if document["evidence_sha256"] != run.evidenceSHA256 {
		return &EvidenceError{message: "completion.json evidence_sha256 does not match validated Evidence."}
	}
	verified := document["verified_source_sha256"].(string)
	current := document["current_source_sha256"].(string)
	if !lowerSHA256(verified) || !lowerSHA256(current) ||
		verified != run.sourceAfterSHA256 || current != run.sourceAfterSHA256 {
		return &EvidenceError{message: "completion.json source digests do not match the ValidateRun-authoritative S1 digest."}
	}
	if document["final_result"] != "pass" {
		return &EvidenceError{message: "completion.json final_result must be 'pass'."}
	}
	completedAt := document["completed_at"].(string)
	parsed, err := time.Parse(completionTimestamp, completedAt)
	if err != nil || parsed.Format(completionTimestamp) != completedAt {
		return &EvidenceError{message: "completion.json completed_at must be a UTC timestamp with six fractional digits and Z suffix."}
	}
	return nil
}

func decodeCompletionObject(contents []byte) (map[string]any, error) {
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("completion record must be an object")
	}
	document := make(map[string]any)
	for decoder.More() {
		rawKey, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := rawKey.(string)
		if !ok {
			return nil, fmt.Errorf("completion record key must be a string")
		}
		if _, duplicate := document[key]; duplicate {
			return nil, fmt.Errorf("duplicate completion record key")
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		document[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("completion record object is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return document, nil
}
