// Package releasegate validates release-internal RC acceptance reports used
// only by the Seal release workflow. It does not define or alter any public
// Seal schema.
package releasegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	reportSchemaVersion = 1
	minimumTaskCount    = 20
	maximumTaskCount    = 10_000
	maximumReportBytes  = 1 << 20
	reportTimeLayout    = "2006-01-02T15:04:05Z"
	schemaFilename      = "report-v1.schema.json"
)

var (
	stableTagPattern  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	rcTagPattern      = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-rc\.([1-9][0-9]*)$`)
	reportNamePattern = regexp.MustCompile(
		`^(v[0-9]+\.[0-9]+\.[0-9]+-rc\.[1-9][0-9]*)\.json$`,
	)
	lowerHexCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type reportWire struct {
	SchemaVersion int               `json:"schema_version"`
	Candidate     *candidateWire    `json:"candidate"`
	Window        *windowWire       `json:"window"`
	Privacy       *privacyWire      `json:"privacy"`
	Attestations  *attestationsWire `json:"attestations"`
	Tasks         *[]taskWire       `json:"tasks"`
}

type candidateWire struct {
	RCTag           string `json:"rc_tag"`
	RCCommit        string `json:"rc_commit"`
	TargetStableTag string `json:"target_stable_tag"`
}

type windowWire struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

type privacyWire struct {
	Anonymous                     *bool `json:"anonymous"`
	ContainsRawLogs               *bool `json:"contains_raw_logs"`
	ContainsSourceContent         *bool `json:"contains_source_content"`
	ContainsRepositoryIdentifiers *bool `json:"contains_repository_identifiers"`
	ContainsUserIdentifiers       *bool `json:"contains_user_identifiers"`
}

type attestationsWire struct {
	AllEligibleTasksRecordedConsecutively *bool `json:"all_eligible_tasks_recorded_consecutively"`
	AllEntriesAreRealUserWork             *bool `json:"all_entries_are_real_user_work"`
	ExactCandidateUsed                    *bool `json:"exact_candidate_used"`
	CriticalFixResetsTheWindow            *bool `json:"critical_fix_resets_the_window"`
}

type taskWire struct {
	Ordinal                         int    `json:"ordinal"`
	ObservedAt                      string `json:"observed_at"`
	Interface                       string `json:"interface"`
	Platform                        string `json:"platform"`
	InitialWorktree                 string `json:"initial_worktree"`
	TaskCreatedBeforeImplementation *bool  `json:"task_created_before_implementation"`
	ExactTaskRunBindingPreserved    *bool  `json:"exact_task_run_binding_preserved"`
	EvidenceResult                  string `json:"evidence_result"`
	MechanicalResult                string `json:"mechanical_result"`
	CompletionResult                string `json:"completion_result"`
	CheckCount                      *int   `json:"check_count"`
	OptionalCheckCount              *int   `json:"optional_check_count"`
	ChecksDurationMS                *int64 `json:"checks_duration_ms"`
	SealToolCallCount               *int   `json:"seal_tool_call_count"`
	SealDurationMS                  *int64 `json:"seal_duration_ms"`
	ResultUnderstoodWithoutFollowup *bool  `json:"result_understood_without_followup"`
	FalseAcceptance                 *bool  `json:"false_acceptance"`
	EvidenceCorruptionBypass        *bool  `json:"evidence_corruption_bypass"`
	SourceBindingBypass             *bool  `json:"source_binding_bypass"`
	FalseSourceMismatch             *bool  `json:"false_source_mismatch"`
	WrongChangeAttribution          *bool  `json:"wrong_change_attribution"`
	WrongPluginRouting              *bool  `json:"wrong_plugin_routing"`
}

// Report is the validated release-gate projection of one report file.
type Report struct {
	Candidate Candidate
	Window    Window
	Tasks     []Task
}

// Candidate binds every counted Task to one exact RC and source surface.
type Candidate struct {
	RCTag           string
	RCCommit        string
	TargetStableTag string
}

// Window is the consecutive real-user observation interval.
type Window struct {
	StartedAt time.Time
	EndedAt   time.Time
}

// Task contains only anonymous, bounded facts needed by the release gate.
type Task struct {
	Ordinal             int
	ObservedAt          time.Time
	FalseSourceMismatch bool
}

// ValidateSyntaxDirectory validates all checked-in report instances while
// allowing an in-progress report to contain fewer than 20 Tasks.
func ValidateSyntaxDirectory(reportsDirectory string) (int, error) {
	info, err := os.Lstat(reportsDirectory)
	if err != nil {
		return 0, fmt.Errorf("inspect acceptance report directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, fmt.Errorf("acceptance report directory must be a real directory")
	}
	if err := validateSchemaDocument(filepath.Join(reportsDirectory, schemaFilename)); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(reportsDirectory)
	if err != nil {
		return 0, fmt.Errorf("read acceptance report directory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if name == schemaFilename || name == "README.md" {
			continue
		}
		if entry.IsDir() {
			return 0, fmt.Errorf("unexpected directory in acceptance report directory: %s", name)
		}
		if !reportNamePattern.MatchString(name) {
			return 0, fmt.Errorf("unexpected acceptance report file: %s", name)
		}
		if _, err := readReport(filepath.Join(reportsDirectory, name), false); err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		count++
	}
	return count, nil
}

func validateSchemaDocument(path string) error {
	contents, err := readBoundedRegularFile(path)
	if err != nil {
		return fmt.Errorf("read acceptance report schema: %w", err)
	}
	if !utf8.Valid(contents) {
		return fmt.Errorf("acceptance report schema must be valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(contents); err != nil {
		return fmt.Errorf("acceptance report schema is invalid: %w", err)
	}
	return nil
}

func readReport(path string, requireComplete bool) (*Report, error) {
	contents, err := readBoundedRegularFile(path)
	if err != nil {
		return nil, err
	}
	return readReportContents(filepath.Base(path), contents, requireComplete)
}

func readReportContents(filename string, contents []byte, requireComplete bool) (*Report, error) {
	if len(contents) > maximumReportBytes {
		return nil, fmt.Errorf("exceeds the %d-byte safety limit", maximumReportBytes)
	}
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("report must be valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(contents); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var wire reportWire
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode report: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return validateReportWire(filename, wire, requireComplete)
}

func readBoundedRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("must be a regular file, not a symlink or special file")
	}
	if info.Size() > maximumReportBytes {
		return nil, fmt.Errorf("exceeds the %d-byte safety limit", maximumReportBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(contents) > maximumReportBytes {
		return nil, fmt.Errorf("exceeds the %d-byte safety limit", maximumReportBytes)
	}
	return contents, nil
}

func validateReportWire(filename string, wire reportWire, requireComplete bool) (*Report, error) {
	if wire.SchemaVersion != reportSchemaVersion {
		return nil, fmt.Errorf("schema_version must be %d", reportSchemaVersion)
	}
	if wire.Candidate == nil || wire.Window == nil || wire.Privacy == nil || wire.Attestations == nil || wire.Tasks == nil {
		return nil, fmt.Errorf("candidate, window, privacy, attestations, and tasks are required")
	}
	candidate, err := validateCandidate(filename, *wire.Candidate)
	if err != nil {
		return nil, err
	}
	window, err := validateWindow(*wire.Window)
	if err != nil {
		return nil, err
	}
	if err := validatePrivacy(*wire.Privacy); err != nil {
		return nil, err
	}
	if err := validateAttestations(*wire.Attestations); err != nil {
		return nil, err
	}
	tasks, err := validateTasks(*wire.Tasks, window, requireComplete)
	if err != nil {
		return nil, err
	}
	return &Report{Candidate: candidate, Window: window, Tasks: tasks}, nil
}

func validateCandidate(filename string, wire candidateWire) (Candidate, error) {
	baseTag, _, ok := parseRCTag(wire.RCTag)
	if !ok {
		return Candidate{}, fmt.Errorf("candidate.rc_tag must be an RC tag")
	}
	wantFilename := wire.RCTag + ".json"
	if filename != wantFilename {
		return Candidate{}, fmt.Errorf("report filename must be %s", wantFilename)
	}
	if wire.TargetStableTag != baseTag || !stableTagPattern.MatchString(wire.TargetStableTag) {
		return Candidate{}, fmt.Errorf("candidate.target_stable_tag must be %s", baseTag)
	}
	if !lowerHexCommitPattern.MatchString(wire.RCCommit) {
		return Candidate{}, fmt.Errorf("candidate.rc_commit must be a lowercase 40- or 64-character Git object id")
	}
	return Candidate{
		RCTag:           wire.RCTag,
		RCCommit:        wire.RCCommit,
		TargetStableTag: wire.TargetStableTag,
	}, nil
}

func validateWindow(wire windowWire) (Window, error) {
	started, err := parseReportTime(wire.StartedAt, "window.started_at")
	if err != nil {
		return Window{}, err
	}
	ended, err := parseReportTime(wire.EndedAt, "window.ended_at")
	if err != nil {
		return Window{}, err
	}
	if ended.Before(started) {
		return Window{}, fmt.Errorf("window.ended_at must not precede window.started_at")
	}
	return Window{StartedAt: started, EndedAt: ended}, nil
}

func validatePrivacy(wire privacyWire) error {
	if err := requireBoolean(wire.Anonymous, true, "privacy.anonymous"); err != nil {
		return err
	}
	checks := []struct {
		name  string
		value *bool
	}{
		{name: "privacy.contains_raw_logs", value: wire.ContainsRawLogs},
		{name: "privacy.contains_source_content", value: wire.ContainsSourceContent},
		{name: "privacy.contains_repository_identifiers", value: wire.ContainsRepositoryIdentifiers},
		{name: "privacy.contains_user_identifiers", value: wire.ContainsUserIdentifiers},
	}
	for _, check := range checks {
		if err := requireBoolean(check.value, false, check.name); err != nil {
			return err
		}
	}
	return nil
}

func validateAttestations(wire attestationsWire) error {
	checks := []struct {
		name  string
		value *bool
	}{
		{name: "attestations.all_eligible_tasks_recorded_consecutively", value: wire.AllEligibleTasksRecordedConsecutively},
		{name: "attestations.all_entries_are_real_user_work", value: wire.AllEntriesAreRealUserWork},
		{name: "attestations.exact_candidate_used", value: wire.ExactCandidateUsed},
		{name: "attestations.critical_fix_resets_the_window", value: wire.CriticalFixResetsTheWindow},
	}
	for _, check := range checks {
		if err := requireBoolean(check.value, true, check.name); err != nil {
			return err
		}
	}
	return nil
}

func validateTasks(wires []taskWire, window Window, requireComplete bool) ([]Task, error) {
	if len(wires) > maximumTaskCount {
		return nil, fmt.Errorf("tasks must contain at most %d entries", maximumTaskCount)
	}
	if requireComplete && len(wires) < minimumTaskCount {
		return nil, fmt.Errorf("stable release requires at least %d Tasks; report contains %d", minimumTaskCount, len(wires))
	}
	validated := make([]Task, len(wires))
	falseMismatchCount := 0
	var previous time.Time
	for index, wire := range wires {
		ordinal := index + 1
		context := fmt.Sprintf("tasks[%d]", index)
		if wire.Ordinal != ordinal {
			return nil, fmt.Errorf("%s.ordinal must be %d", context, ordinal)
		}
		observed, err := parseReportTime(wire.ObservedAt, context+".observed_at")
		if err != nil {
			return nil, err
		}
		if observed.Before(window.StartedAt) || observed.After(window.EndedAt) {
			return nil, fmt.Errorf("%s.observed_at must be inside the report window", context)
		}
		if !previous.IsZero() && observed.Before(previous) {
			return nil, fmt.Errorf("%s.observed_at must not precede the prior Task", context)
		}
		previous = observed
		if !oneOf(wire.Interface, "core-cli", "plugin-explicit", "plugin-implicit") {
			return nil, fmt.Errorf("%s.interface is unsupported", context)
		}
		if !oneOf(wire.Platform, "linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64") {
			return nil, fmt.Errorf("%s.platform is unsupported", context)
		}
		if !oneOf(wire.InitialWorktree, "clean", "dirty") {
			return nil, fmt.Errorf("%s.initial_worktree is unsupported", context)
		}
		if requireComplete && wire.Interface == "plugin-implicit" && wire.InitialWorktree != "clean" {
			return nil, fmt.Errorf("%s stable release requires implicit activation from a clean initial worktree", context)
		}
		if err := requirePresentBoolean(wire.TaskCreatedBeforeImplementation, context+".task_created_before_implementation"); err != nil {
			return nil, err
		}
		if err := requirePresentBoolean(wire.ExactTaskRunBindingPreserved, context+".exact_task_run_binding_preserved"); err != nil {
			return nil, err
		}
		if err := validateMetrics(context, wire); err != nil {
			return nil, err
		}
		if err := validateResultConsistency(context, wire); err != nil {
			return nil, err
		}
		safetyChecks := []struct {
			name  string
			value *bool
		}{
			{name: context + ".false_acceptance", value: wire.FalseAcceptance},
			{name: context + ".evidence_corruption_bypass", value: wire.EvidenceCorruptionBypass},
			{name: context + ".source_binding_bypass", value: wire.SourceBindingBypass},
			{name: context + ".wrong_change_attribution", value: wire.WrongChangeAttribution},
			{name: context + ".wrong_plugin_routing", value: wire.WrongPluginRouting},
		}
		for _, check := range safetyChecks {
			if err := requirePresentBoolean(check.value, check.name); err != nil {
				return nil, err
			}
			if requireComplete && *check.value {
				return nil, fmt.Errorf("%s must be false for a stable release", check.name)
			}
		}
		if wire.FalseSourceMismatch == nil {
			return nil, fmt.Errorf("%s.false_source_mismatch is required", context)
		}
		if *wire.FalseSourceMismatch {
			falseMismatchCount++
		}
		validated[index] = Task{Ordinal: ordinal, ObservedAt: observed, FalseSourceMismatch: *wire.FalseSourceMismatch}
	}
	if requireComplete && falseMismatchCount > 1 {
		return nil, fmt.Errorf("stable report contains %d false source mismatches; at most one is allowed", falseMismatchCount)
	}
	return validated, nil
}

func validateResultConsistency(context string, wire taskWire) error {
	if !oneOf(wire.EvidenceResult, "recorded", "not-recorded", "indeterminate") {
		return fmt.Errorf("%s.evidence_result is unsupported", context)
	}
	if !oneOf(wire.MechanicalResult, "pass", "fail", "unavailable", "indeterminate") {
		return fmt.Errorf("%s.mechanical_result is unsupported", context)
	}
	if !oneOf(wire.CompletionResult, "accepted", "rejected", "failed", "not-attempted", "indeterminate") {
		return fmt.Errorf("%s.completion_result is unsupported", context)
	}
	if wire.CompletionResult == "accepted" && (wire.EvidenceResult != "recorded" || wire.MechanicalResult != "pass") {
		return fmt.Errorf("%s accepted completion requires recorded Evidence and mechanical pass", context)
	}
	if oneOf(wire.MechanicalResult, "pass", "fail") && wire.EvidenceResult != "recorded" {
		return fmt.Errorf("%s mechanical pass or fail requires recorded Evidence", context)
	}
	if wire.EvidenceResult == "not-recorded" {
		if wire.MechanicalResult != "unavailable" || oneOf(wire.CompletionResult, "accepted", "rejected") {
			return fmt.Errorf("%s not-recorded Evidence requires unavailable mechanics and a non-acceptance completion", context)
		}
	}
	if wire.EvidenceResult == "indeterminate" {
		if !oneOf(wire.MechanicalResult, "unavailable", "indeterminate") || oneOf(wire.CompletionResult, "accepted", "rejected") {
			return fmt.Errorf("%s indeterminate Evidence requires unavailable or indeterminate mechanics and a non-acceptance completion", context)
		}
	}
	if wire.CompletionResult == "rejected" && wire.EvidenceResult != "recorded" {
		return fmt.Errorf("%s rejected completion requires recorded Evidence", context)
	}
	return nil
}

func validateMetrics(context string, wire taskWire) error {
	if wire.CheckCount == nil || *wire.CheckCount < 0 || *wire.CheckCount > maximumTaskCount {
		return fmt.Errorf("%s.check_count must be an integer from 0 through %d", context, maximumTaskCount)
	}
	if wire.OptionalCheckCount == nil || *wire.OptionalCheckCount < 0 || *wire.OptionalCheckCount > *wire.CheckCount {
		return fmt.Errorf("%s.optional_check_count must be from 0 through check_count", context)
	}
	const maximumDurationMS = int64(24 * time.Hour / time.Millisecond)
	if wire.ChecksDurationMS == nil || *wire.ChecksDurationMS < 0 || *wire.ChecksDurationMS > maximumDurationMS {
		return fmt.Errorf("%s.checks_duration_ms must be from 0 through %d", context, maximumDurationMS)
	}
	if wire.SealToolCallCount == nil || *wire.SealToolCallCount < 0 || *wire.SealToolCallCount > maximumTaskCount {
		return fmt.Errorf("%s.seal_tool_call_count must be an integer from 0 through %d", context, maximumTaskCount)
	}
	if wire.SealDurationMS == nil || *wire.SealDurationMS < 0 || *wire.SealDurationMS > maximumDurationMS {
		return fmt.Errorf("%s.seal_duration_ms must be from 0 through %d", context, maximumDurationMS)
	}
	if (*wire.TaskCreatedBeforeImplementation || wire.EvidenceResult == "recorded") && (*wire.CheckCount == 0 || *wire.SealToolCallCount == 0) {
		return fmt.Errorf("%s a Task created before implementation or recorded Evidence requires at least one selected check and one Seal tool call", context)
	}
	if wire.ResultUnderstoodWithoutFollowup == nil {
		return fmt.Errorf("%s.result_understood_without_followup is required", context)
	}
	return nil
}

func requireBoolean(value *bool, want bool, name string) error {
	if value == nil {
		return fmt.Errorf("%s is required", name)
	}
	if *value != want {
		return fmt.Errorf("%s must be %t", name, want)
	}
	return nil
}

func requirePresentBoolean(value *bool, name string) error {
	if value == nil {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func parseReportTime(value, name string) (time.Time, error) {
	parsed, err := time.Parse(reportTimeLayout, value)
	if err != nil || parsed.Format(reportTimeLayout) != value {
		return time.Time{}, fmt.Errorf("%s must use UTC second precision %s", name, reportTimeLayout)
	}
	return parsed, nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func parseRCTag(value string) (string, int, bool) {
	matches := rcTagPattern.FindStringSubmatch(value)
	if matches == nil {
		return "", 0, false
	}
	number, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", 0, false
	}
	return matches[1], number, true
}

func rejectDuplicateJSONKeys(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, "$"); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func walkJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON at %s: %w", path, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode JSON object at %s: %w", path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key at %s is not a string", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode JSON object at %s: %w", path, err)
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("decode JSON object at %s: unexpected closing delimiter", path)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := walkJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode JSON array at %s: %w", path, err)
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("decode JSON array at %s: unexpected closing delimiter", path)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("report contains trailing JSON data")
		}
		return fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return nil
}
