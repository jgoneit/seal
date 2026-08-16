// Package runstate creates, validates, and projects explicitly identified
// Acceptance Runs. It never infers a latest identity, repairs work, or invokes
// another Toolkit module.
package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	taskSchemaVersion       = 1
	evidenceSchemaVersion   = 2
	summarySchemaVersion    = 1
	defaultCheckTimeoutSecs = 300
)

// ErrorKind is the stable public category of a handled Run query error.
type ErrorKind uint8

const (
	KindUnknown ErrorKind = iota
	KindInvalidInput
	KindRepository
	KindEvidence
	KindRuntime
)

// IdentityError reports invalid or inconsistent requested and stored identity.
type IdentityError struct{ message string }

func (e *IdentityError) Error() string { return e.message }

// RepositoryError reports that an enclosing repository could not be resolved.
type RepositoryError struct{ message string }

func (e *RepositoryError) Error() string { return e.message }

// EvidenceError reports missing, unsafe, unsupported, or contradictory Evidence.
type EvidenceError struct{ message string }

func (e *EvidenceError) Error() string { return e.message }

// RuntimeError reports a frozen-runtime JSON conversion failure that is not a
// handled identity, repository, or Evidence-contract error.
type RuntimeError struct{ message string }

func (e *RuntimeError) Error() string { return e.message }

// KindOf returns the stable category for an error returned by ValidateRun.
func KindOf(err error) ErrorKind {
	var identity *IdentityError
	var repository *RepositoryError
	var evidence *EvidenceError
	var runtimeError *RuntimeError
	switch {
	case errors.As(err, &identity):
		return KindInvalidInput
	case errors.As(err, &repository):
		return KindRepository
	case errors.As(err, &evidence):
		return KindEvidence
	case errors.As(err, &runtimeError):
		return KindRuntime
	default:
		return KindUnknown
	}
}

// ValidatedRun is an immutable-by-convention result of the package's sole
// stored-Run integrity authority. Its fields are intentionally unexported.
type ValidatedRun struct {
	taskID                   string
	runID                    string
	evidenceSHA256           string
	mechanicalResult         string
	scopePass                bool
	scopeViolations          []ScopeViolation
	requiredChecksPass       bool
	sourceStableDuringChecks bool
	checks                   []Check
}

// Summary is the public read-only validated-run-summary/v1 projection.
// Field declaration order matches the deterministic reference JSON order.
type Summary struct {
	Checks                   []Check          `json:"checks"`
	EvidenceSHA256           string           `json:"evidence_sha256"`
	MechanicalResult         string           `json:"mechanical_result"`
	RequiredChecksPass       bool             `json:"required_checks_pass"`
	RunID                    string           `json:"run_id"`
	SchemaVersion            int              `json:"schema_version"`
	ScopePass                bool             `json:"scope_pass"`
	ScopeViolations          []ScopeViolation `json:"scope_violations"`
	SourceStableDuringChecks bool             `json:"source_stable_during_checks"`
	TaskID                   string           `json:"task_id"`
}

// Check is the public projection of one validated saved check result.
type Check struct {
	ExitCode *json.Number `json:"exit_code"`
	Name     string       `json:"name"`
	Passed   bool         `json:"passed"`
	Required any          `json:"required"`
	TimedOut bool         `json:"timed_out"`
}

// ScopeViolation is the public projection of one validated Scope violation.
type ScopeViolation struct {
	Path         string  `json:"path"`
	PreviousPath *string `json:"previous_path"`
	Source       string  `json:"source"`
	Status       string  `json:"status"`
}

// MarshalJSON preserves escaped lone-surrogate paths while retaining the
// reference's sorted public field order.
func (summary Summary) MarshalJSON() ([]byte, error) {
	return canonicalJSONMode(summary.document(), true, false)
}

// ReferenceJSON returns the exact byte-oriented JSON projection used by the
// frozen POSIX reference, including surrogateescape bytes in Git paths.
func (summary Summary) ReferenceJSON() ([]byte, error) {
	return prettyCanonicalJSONMode(summary.document(), false, true)
}

func (summary Summary) document() map[string]any {
	checks := make([]any, len(summary.Checks))
	for index, check := range summary.Checks {
		var exitCode any
		if check.ExitCode != nil {
			exitCode = *check.ExitCode
		}
		checks[index] = map[string]any{
			"exit_code": exitCode,
			"name":      check.Name,
			"passed":    check.Passed,
			"required":  check.Required,
			"timed_out": check.TimedOut,
		}
	}
	violations := make([]any, len(summary.ScopeViolations))
	for index, violation := range summary.ScopeViolations {
		var previous any
		if violation.PreviousPath != nil {
			previous = *violation.PreviousPath
		}
		violations[index] = map[string]any{
			"path":          violation.Path,
			"previous_path": previous,
			"source":        violation.Source,
			"status":        violation.Status,
		}
	}
	return map[string]any{
		"checks":                      checks,
		"evidence_sha256":             summary.EvidenceSHA256,
		"mechanical_result":           summary.MechanicalResult,
		"required_checks_pass":        summary.RequiredChecksPass,
		"run_id":                      summary.RunID,
		"schema_version":              summary.SchemaVersion,
		"scope_pass":                  summary.ScopePass,
		"scope_violations":            violations,
		"source_stable_during_checks": summary.SourceStableDuringChecks,
		"task_id":                     summary.TaskID,
	}
}

// Summary returns a detached projection of this validated Run.
func (run *ValidatedRun) Summary() Summary {
	checks := make([]Check, len(run.checks))
	for index, check := range run.checks {
		checks[index] = cloneCheck(check)
	}
	violations := make([]ScopeViolation, len(run.scopeViolations))
	for index, violation := range run.scopeViolations {
		violations[index] = cloneViolation(violation)
	}
	return Summary{
		Checks:                   checks,
		EvidenceSHA256:           run.evidenceSHA256,
		MechanicalResult:         run.mechanicalResult,
		RequiredChecksPass:       run.requiredChecksPass,
		RunID:                    run.runID,
		SchemaVersion:            summarySchemaVersion,
		ScopePass:                run.scopePass,
		ScopeViolations:          violations,
		SourceStableDuringChecks: run.sourceStableDuringChecks,
		TaskID:                   run.taskID,
	}
}

func cloneCheck(value Check) Check {
	cloned := value
	if value.ExitCode != nil {
		exitCode := *value.ExitCode
		cloned.ExitCode = &exitCode
	}
	cloned.Required = cloneJSON(value.Required)
	return cloned
}

func cloneViolation(value ScopeViolation) ScopeViolation {
	cloned := value
	if value.PreviousPath != nil {
		previous := *value.PreviousPath
		cloned.PreviousPath = &previous
	}
	return cloned
}

// ValidateRun validates exactly one saved Task and Run without executing
// checks, collecting current source, or reading Verdict or completion state.
func ValidateRun(cwd, taskID, runID string) (*ValidatedRun, error) {
	if err := validateIdentity(taskID, "Task"); err != nil {
		return nil, err
	}
	if err := validateIdentity(runID, "Run"); err != nil {
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
	if err := validateTaskSnapshot(task, taskID, "saved Task snapshot"); err != nil {
		return nil, err
	}

	runDirectory, err := validateRunDirectory(repository, taskID, runID)
	if err != nil {
		return nil, err
	}
	return validateRunAt(repository, task, taskID, runID, runDirectory)
}

func validateRunAt(
	repository string,
	task jsonObject,
	taskID string,
	runID string,
	runDirectory string,
) (*ValidatedRun, error) {
	documents, err := validateDocuments(runDirectory, task, taskID, runID)
	if err != nil {
		return nil, err
	}
	evidenceSHA256, err := validateManifest(
		runDirectory,
		taskID,
		runID,
		documents.expectedFiles,
	)
	if err != nil {
		return nil, err
	}

	return &ValidatedRun{
		taskID:                   taskID,
		runID:                    runID,
		evidenceSHA256:           evidenceSHA256,
		mechanicalResult:         documents.mechanicalResult,
		scopePass:                documents.scopePass,
		scopeViolations:          documents.scopeViolations,
		requiredChecksPass:       documents.requiredChecksPass,
		sourceStableDuringChecks: documents.sourceStableDuringChecks,
		checks:                   documents.checkSummaries,
	}, nil
}

func validateIdentity(value, label string) error {
	if value == "" {
		return &IdentityError{message: fmt.Sprintf("%s id must be a non-empty string.", label)}
	}
	if !asciiAlphaNumeric(value[0]) {
		return &IdentityError{message: fmt.Sprintf(
			"%s id must begin with an alphanumeric character and contain only letters, numbers, underscores, or hyphens.",
			label,
		)}
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !asciiAlphaNumeric(character) && character != '_' && character != '-' {
			return &IdentityError{message: fmt.Sprintf(
				"%s id must contain only letters, numbers, underscores, or hyphens.",
				label,
			)}
		}
	}
	return nil
}

func asciiAlphaNumeric(character byte) bool {
	return character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9'
}

func findRepositoryRoot(cwd string) (string, error) {
	workingDirectory := cwd
	if workingDirectory == "" {
		var err error
		workingDirectory, err = os.Getwd()
		if err != nil {
			return "", &RepositoryError{message: "Task commands must run inside a Git repository."}
		}
	}
	absolute, err := filepath.Abs(workingDirectory)
	if err != nil {
		return "", &RepositoryError{message: "Task commands must run inside a Git repository."}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", &RepositoryError{message: "Task commands must run inside a Git repository."}
	}
	start := filepath.Clean(resolved)
	if info, statErr := os.Stat(start); statErr != nil || !info.IsDir() {
		start = filepath.Dir(start)
	}
	for candidate := start; ; candidate = filepath.Dir(candidate) {
		if _, statErr := os.Stat(filepath.Join(candidate, ".git")); statErr == nil {
			return candidate, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	return "", &RepositoryError{message: "Task commands must run inside a Git repository."}
}

func readSavedTask(repository, taskID string) (jsonObject, error) {
	path := filepath.Join(repository, ".seal", "tasks", taskID+".json")
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &IdentityError{message: fmt.Sprintf(
				"Saved Task snapshot '%s' does not exist.",
				taskID,
			)}
		}
		return nil, &IdentityError{message: fmt.Sprintf(
			"Saved Task snapshot '%s' is unreadable or invalid JSON.",
			taskID,
		)}
	}
	value, err := decodeJSONObject(contents)
	if err != nil {
		if isStandardJSONDepthLimit(err) {
			return nil, &RuntimeError{message: fmt.Sprintf(
				"Saved Task snapshot '%s' exceeds the supported JSON nesting depth.",
				taskID,
			)}
		}
		if KindOf(err) == KindRuntime {
			return nil, err
		}
		return nil, &IdentityError{message: fmt.Sprintf(
			"Saved Task snapshot '%s' is unreadable or invalid JSON.",
			taskID,
		)}
	}
	return value, nil
}

func validateRunDirectory(repository, taskID, runID string) (string, error) {
	chain := []string{
		filepath.Join(repository, ".seal"),
		filepath.Join(repository, ".seal", "evidence"),
		filepath.Join(repository, ".seal", "evidence", taskID),
		filepath.Join(repository, ".seal", "evidence", taskID, runID),
	}
	for _, path := range chain {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", &EvidenceError{message: fmt.Sprintf(
				"Evidence Run directory is missing or unsafe: %s/%s.",
				taskID,
				runID,
			)}
		}
	}
	logical := chain[len(chain)-1]
	info, err := os.Stat(logical)
	if err != nil || !info.IsDir() {
		return "", &EvidenceError{message: fmt.Sprintf(
			"Evidence Run directory is missing or unsafe: %s/%s.",
			taskID,
			runID,
		)}
	}
	resolved, err := filepath.EvalSymlinks(logical)
	if err != nil {
		return "", &EvidenceError{message: fmt.Sprintf(
			"Evidence Run directory is missing or unreadable: %s/%s.",
			taskID,
			runID,
		)}
	}
	inside, err := pathInside(repository, resolved)
	if err != nil || !inside {
		return "", &EvidenceError{message: "Evidence Run directory must remain inside the repository."}
	}
	return resolved, nil
}

func pathInside(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative), nil
}
