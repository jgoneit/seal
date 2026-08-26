package releasegate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckedInAcceptanceReportsValidate(t *testing.T) {
	_, err := ValidateSyntaxDirectory(filepath.Join("..", "..", "release", "acceptance"))
	if err != nil {
		t.Fatalf("ValidateSyntaxDirectory() error = %v", err)
	}
}

func TestSyntaxAllowsNineteenWhileStableRequiresTwentyOrMore(t *testing.T) {
	directory := newReportDirectory(t)
	path := filepath.Join(directory, "v1.2.3-rc.4.json")
	writeReportDocument(t, path, validReportDocument(19))

	count, err := ValidateSyntaxDirectory(directory)
	if err != nil || count != 1 {
		t.Fatalf("ValidateSyntaxDirectory() = %d, %v", count, err)
	}
	if _, err := readReport(path, true); err == nil || !strings.Contains(err.Error(), "at least 20") {
		t.Fatalf("readReport(19 complete Tasks) error = %v, want minimum-count failure", err)
	}

	for _, taskCount := range []int{20, 21} {
		writeReportDocument(t, path, validReportDocument(taskCount))
		if _, err := readReport(path, true); err != nil {
			t.Fatalf("readReport(%d complete Tasks) error = %v", taskCount, err)
		}
	}
}

func TestReportHasBoundedHighTaskMaximum(t *testing.T) {
	_, err := validateTasks(make([]taskWire, maximumTaskCount+1), Window{}, false)
	if err == nil || !strings.Contains(err.Error(), "at most 10000") {
		t.Fatalf("validateTasks() error = %v, want bounded-maximum failure", err)
	}
}

func TestReportStructuralInvariants(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "unknown field", mutate: func(document map[string]any) { document["notes"] = "not allowed" }, want: "unknown field"},
		{name: "privacy anonymous", mutate: setPrivacy("anonymous", false), want: "privacy.anonymous must be true"},
		{name: "privacy raw logs", mutate: setPrivacy("contains_raw_logs", true), want: "privacy.contains_raw_logs must be false"},
		{name: "privacy source content", mutate: setPrivacy("contains_source_content", true), want: "privacy.contains_source_content must be false"},
		{name: "privacy repository identifiers", mutate: setPrivacy("contains_repository_identifiers", true), want: "privacy.contains_repository_identifiers must be false"},
		{name: "privacy user identifiers", mutate: setPrivacy("contains_user_identifiers", true), want: "privacy.contains_user_identifiers must be false"},
		{
			name: "timestamp precision",
			mutate: func(document map[string]any) {
				document["window"].(map[string]any)["started_at"] = "2026-09-01T00:00:00.000Z"
			},
			want: "window.started_at must use UTC second precision",
		},
		{
			name: "window end before start",
			mutate: func(document map[string]any) {
				document["window"].(map[string]any)["ended_at"] = "2026-08-31T23:59:59Z"
			},
			want: "window.ended_at must not precede window.started_at",
		},
		{
			name: "Task outside window",
			mutate: func(document map[string]any) {
				firstTask(document)["observed_at"] = "2026-09-01T01:00:01Z"
			},
			want: "observed_at must be inside the report window",
		},
		{
			name: "Task timestamps out of order",
			mutate: func(document map[string]any) {
				tasks := document["tasks"].([]any)
				tasks[0].(map[string]any)["observed_at"] = "2026-09-01T00:20:00Z"
				tasks[1].(map[string]any)["observed_at"] = "2026-09-01T00:10:00Z"
			},
			want: "observed_at must not precede the prior Task",
		},
		{
			name: "nonconsecutive ordinal",
			mutate: func(document map[string]any) {
				document["tasks"].([]any)[1].(map[string]any)["ordinal"] = 3
			},
			want: "tasks[1].ordinal must be 2",
		},
		{name: "unsupported interface", mutate: setFirstTask("interface", "web"), want: "interface is unsupported"},
		{name: "unsupported platform", mutate: setFirstTask("platform", "plan9-amd64"), want: "platform is unsupported"},
		{name: "unsupported worktree", mutate: setFirstTask("initial_worktree", "dirty-unattributed"), want: "initial_worktree is unsupported"},
		{name: "unsupported Evidence result", mutate: setFirstTask("evidence_result", "missing"), want: "evidence_result is unsupported"},
		{name: "unsupported mechanical result", mutate: setFirstTask("mechanical_result", "unknown"), want: "mechanical_result is unsupported"},
		{name: "unsupported Completion result", mutate: setFirstTask("completion_result", "completed"), want: "completion_result is unsupported"},
		{name: "optional checks exceed selected", mutate: setFirstTask("optional_check_count", 3), want: "optional_check_count must be from 0 through check_count"},
		{name: "negative check duration", mutate: setFirstTask("checks_duration_ms", -1), want: "checks_duration_ms must be from 0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(3)
			test.mutate(document)
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			_, err := readReport(path, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readReport() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSyntaxAcceptsTruthfulPilotIncidentsThatBlockStable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "false acceptance", mutate: setFirstTask("false_acceptance", true), want: "false_acceptance must be false for a stable release"},
		{name: "Evidence corruption bypass", mutate: setFirstTask("evidence_corruption_bypass", true), want: "evidence_corruption_bypass must be false for a stable release"},
		{name: "source binding bypass", mutate: setFirstTask("source_binding_bypass", true), want: "source_binding_bypass must be false for a stable release"},
		{name: "wrong attribution", mutate: setFirstTask("wrong_change_attribution", true), want: "wrong_change_attribution must be false for a stable release"},
		{name: "wrong routing", mutate: setFirstTask("wrong_plugin_routing", true), want: "wrong_plugin_routing must be false for a stable release"},
		{
			name: "two false source mismatches",
			mutate: func(document map[string]any) {
				tasks := document["tasks"].([]any)
				tasks[0].(map[string]any)["false_source_mismatch"] = true
				tasks[1].(map[string]any)["false_source_mismatch"] = true
			},
			want: "at most one is allowed",
		},
		{
			name: "implicit use from dirty worktree",
			mutate: func(document map[string]any) {
				task := firstTask(document)
				task["interface"] = "plugin-implicit"
				task["initial_worktree"] = "dirty"
			},
			want: "stable release requires implicit activation from a clean initial worktree",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(minimumTaskCount)
			test.mutate(document)
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			if _, err := readReport(path, false); err != nil {
				t.Fatalf("syntax validation rejected truthful pilot: %v", err)
			}
			if _, err := readReport(path, true); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("stable validation error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTaskCreationAndBindingObservationsRemainRepresentable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "routing stop before Task",
			mutate: func(document map[string]any) {
				task := firstTask(document)
				task["task_created_before_implementation"] = false
				task["exact_task_run_binding_preserved"] = false
				task["check_count"] = 0
				task["optional_check_count"] = 0
				task["seal_tool_call_count"] = 0
				setTaskResults(task, "not-recorded", "unavailable", "not-attempted")
			},
		},
		{
			name: "protocol observations false despite recorded Evidence",
			mutate: func(document map[string]any) {
				task := firstTask(document)
				task["task_created_before_implementation"] = false
				task["exact_task_run_binding_preserved"] = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(minimumTaskCount)
			test.mutate(document)
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			for _, requireComplete := range []bool{false, true} {
				if _, err := readReport(path, requireComplete); err != nil {
					t.Fatalf("readReport(requireComplete=%t) error = %v", requireComplete, err)
				}
			}
		})
	}
}

func TestCreatedTaskOrRecordedEvidenceRequiresChecksAndSealToolCall(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "zero checks",
			mutate: func(document map[string]any) {
				task := firstTask(document)
				task["check_count"] = 0
				task["optional_check_count"] = 0
			},
		},
		{name: "zero tool calls", mutate: setFirstTask("seal_tool_call_count", 0)},
		{
			name: "recorded Evidence after implementation with zero counts",
			mutate: func(document map[string]any) {
				task := firstTask(document)
				task["task_created_before_implementation"] = false
				task["check_count"] = 0
				task["optional_check_count"] = 0
				task["seal_tool_call_count"] = 0
				setTaskResults(task, "recorded", "pass", "accepted")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(1)
			test.mutate(document)
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			_, err := readReport(path, false)
			if err == nil || !strings.Contains(err.Error(), "a Task created before implementation or recorded Evidence requires at least one selected check and one Seal tool call") {
				t.Fatalf("readReport() error = %v", err)
			}
		})
	}
}

func TestValidResultCombinations(t *testing.T) {
	tests := []struct {
		name       string
		evidence   string
		mechanical string
		completion string
	}{
		{name: "accepted", evidence: "recorded", mechanical: "pass", completion: "accepted"},
		{name: "policy rejection", evidence: "recorded", mechanical: "pass", completion: "rejected"},
		{name: "mechanical failure", evidence: "recorded", mechanical: "fail", completion: "failed"},
		{name: "operational failure after Evidence", evidence: "recorded", mechanical: "indeterminate", completion: "failed"},
		{name: "no Evidence", evidence: "not-recorded", mechanical: "unavailable", completion: "not-attempted"},
		{name: "Evidence unavailable", evidence: "not-recorded", mechanical: "unavailable", completion: "failed"},
		{name: "Evidence indeterminate", evidence: "indeterminate", mechanical: "indeterminate", completion: "indeterminate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(1)
			setTaskResults(firstTask(document), test.evidence, test.mechanical, test.completion)
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			if _, err := readReport(path, false); err != nil {
				t.Fatalf("readReport() error = %v", err)
			}
		})
	}
}

func TestRejectsUnsoundResultCombinations(t *testing.T) {
	tests := []struct {
		name       string
		evidence   string
		mechanical string
		completion string
		want       string
	}{
		{name: "accepted without Evidence", evidence: "not-recorded", mechanical: "unavailable", completion: "accepted", want: "accepted completion requires"},
		{name: "accepted mechanical failure", evidence: "recorded", mechanical: "fail", completion: "accepted", want: "accepted completion requires"},
		{name: "mechanical pass without Evidence", evidence: "not-recorded", mechanical: "pass", completion: "failed", want: "mechanical pass or fail requires"},
		{name: "mechanical fail with indeterminate Evidence", evidence: "indeterminate", mechanical: "fail", completion: "failed", want: "mechanical pass or fail requires"},
		{name: "not-recorded with indeterminate mechanics", evidence: "not-recorded", mechanical: "indeterminate", completion: "failed", want: "not-recorded Evidence requires"},
		{name: "not-recorded rejected", evidence: "not-recorded", mechanical: "unavailable", completion: "rejected", want: "not-recorded Evidence requires"},
		{name: "indeterminate Evidence with accepted Completion", evidence: "indeterminate", mechanical: "unavailable", completion: "accepted", want: "accepted completion requires"},
		{name: "indeterminate Evidence rejected", evidence: "indeterminate", mechanical: "indeterminate", completion: "rejected", want: "indeterminate Evidence requires"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(1)
			setTaskResults(firstTask(document), test.evidence, test.mechanical, test.completion)
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			_, err := readReport(path, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readReport() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReportRejectsWrongCandidateRCTagAndStableTag(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "wrong RC tag for filename", mutate: func(candidate map[string]any) { candidate["rc_tag"] = "v1.2.3-rc.3" }, want: "report filename must be v1.2.3-rc.3.json"},
		{name: "wrong target stable tag", mutate: func(candidate map[string]any) { candidate["target_stable_tag"] = "v1.2.4" }, want: "candidate.target_stable_tag must be v1.2.3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validReportDocument(1)
			test.mutate(document["candidate"].(map[string]any))
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			writeReportDocument(t, path, document)
			_, err := readReport(path, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readReport() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestStableReportAllowsOneFalseSourceMismatch(t *testing.T) {
	document := validReportDocument(minimumTaskCount)
	document["tasks"].([]any)[7].(map[string]any)["false_source_mismatch"] = true
	path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
	writeReportDocument(t, path, document)
	if _, err := readReport(path, true); err != nil {
		t.Fatalf("readReport() error = %v", err)
	}
}

func TestReportRejectsDuplicateKeysAndTrailingData(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "duplicate", contents: `{"schema_version":1,"schema_version":1}`, want: "duplicate JSON key"},
		{name: "trailing", contents: `{}` + "\n{}", want: "trailing JSON data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v1.2.3-rc.4.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readReport(path, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readReport() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReportRejectsInvalidUTF8(t *testing.T) {
	_, err := readReportContents("v1.2.3-rc.4.json", []byte{0xff}, false)
	if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("readReportContents() error = %v, want UTF-8 rejection", err)
	}
}

func TestReportFileSizeLimitIsInclusiveAtOneMiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, make([]byte, maximumReportBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(path); err != nil {
		t.Fatalf("readBoundedRegularFile(exact limit) error = %v", err)
	}
	if err := os.WriteFile(path, make([]byte, maximumReportBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(path); err == nil || !strings.Contains(err.Error(), "1048576-byte safety limit") {
		t.Fatalf("readBoundedRegularFile(over limit) error = %v", err)
	}
	if _, err := readReportContents("v1.2.3-rc.4.json", make([]byte, maximumReportBytes+1), false); err == nil || !strings.Contains(err.Error(), "1048576-byte safety limit") {
		t.Fatalf("readReportContents(over limit) error = %v", err)
	}
}

func TestSyntaxDirectoryRejectsUnexpectedAndSymlinkedReports(t *testing.T) {
	directory := newReportDirectory(t)
	if err := os.WriteFile(filepath.Join(directory, "notes.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSyntaxDirectory(directory); err == nil || !strings.Contains(err.Error(), "unexpected acceptance report file") {
		t.Fatalf("unexpected-file error = %v", err)
	}

	directory = newReportDirectory(t)
	if err := os.Mkdir(filepath.Join(directory, "v1.2.3-rc.4.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSyntaxDirectory(directory); err == nil || !strings.Contains(err.Error(), "unexpected directory") {
		t.Fatalf("directory-entry error = %v", err)
	}

	directory = newReportDirectory(t)
	target := filepath.Join(t.TempDir(), "target.json")
	writeReportDocument(t, target, validReportDocument(1))
	link := filepath.Join(directory, "v1.2.3-rc.4.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ValidateSyntaxDirectory(directory); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func newReportDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, schemaFilename), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func validReportDocument(taskCount int) map[string]any {
	tasks := make([]any, taskCount)
	for index := range taskCount {
		tasks[index] = map[string]any{
			"ordinal":                            index + 1,
			"observed_at":                        "2026-09-01T00:10:00Z",
			"interface":                          "core-cli",
			"platform":                           "darwin-arm64",
			"initial_worktree":                   "clean",
			"task_created_before_implementation": true,
			"exact_task_run_binding_preserved":   true,
			"evidence_result":                    "recorded",
			"mechanical_result":                  "pass",
			"completion_result":                  "accepted",
			"check_count":                        2,
			"optional_check_count":               1,
			"checks_duration_ms":                 1500,
			"seal_tool_call_count":               5,
			"seal_duration_ms":                   2300,
			"result_understood_without_followup": true,
			"false_acceptance":                   false,
			"evidence_corruption_bypass":         false,
			"source_binding_bypass":              false,
			"false_source_mismatch":              false,
			"wrong_change_attribution":           false,
			"wrong_plugin_routing":               false,
		}
	}
	return map[string]any{
		"schema_version": 1,
		"candidate": map[string]any{
			"rc_tag":            "v1.2.3-rc.4",
			"rc_commit":         strings.Repeat("a", 40),
			"target_stable_tag": "v1.2.3",
		},
		"window": map[string]any{
			"started_at": "2026-09-01T00:00:00Z",
			"ended_at":   "2026-09-01T01:00:00Z",
		},
		"privacy": map[string]any{
			"anonymous":                       true,
			"contains_raw_logs":               false,
			"contains_source_content":         false,
			"contains_repository_identifiers": false,
			"contains_user_identifiers":       false,
		},
		"attestations": map[string]any{
			"all_eligible_tasks_recorded_consecutively": true,
			"all_entries_are_real_user_work":            true,
			"exact_candidate_used":                      true,
			"critical_fix_resets_the_window":            true,
		},
		"tasks": tasks,
	}
}

func setPrivacy(field string, value bool) func(map[string]any) {
	return func(document map[string]any) {
		document["privacy"].(map[string]any)[field] = value
	}
}

func setFirstTask(field string, value any) func(map[string]any) {
	return func(document map[string]any) {
		firstTask(document)[field] = value
	}
}

func firstTask(document map[string]any) map[string]any {
	return document["tasks"].([]any)[0].(map[string]any)
}

func setTaskResults(task map[string]any, evidence, mechanical, completion string) {
	task["evidence_result"] = evidence
	task["mechanical_result"] = mechanical
	task["completion_result"] = completion
}

func writeReportDocument(t *testing.T, path string, document map[string]any) {
	t.Helper()
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
