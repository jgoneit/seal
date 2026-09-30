package runstate

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jgoneit/seal/internal/pyjson"
)

const exportEntryLimit = 10_000
const exportInventoryEntryLimit = 100_000

// ExportReport is a read-only, content-free projection of stored Runs. It
// reports local record consistency, not tool delivery or current Acceptance.
type ExportReport struct {
	Schema          string        `json:"schema"`
	ExporterVersion string        `json:"exporter_version"`
	ScanComplete    bool          `json:"scan_complete"`
	Tasks           []ExportTask  `json:"tasks"`
	Runs            []ExportRun   `json:"runs"`
	Issues          []ExportIssue `json:"issues"`
}

type ExportTask struct {
	TaskID string `json:"task_id"`
}

type exportHooks struct{ afterScan func() }

type ExportRun struct {
	// Existing canonical Evidence does not persist its producer version. The
	// exporting binary's version must never be attributed to historical Runs.
	RunVersion               *string          `json:"run_version"`
	TaskID                   string           `json:"task_id"`
	RunID                    string           `json:"run_id"`
	EvidenceSHA256           string           `json:"evidence_sha256"`
	Timestamp                *string          `json:"timestamp"`
	MechanicalResult         string           `json:"mechanical_result"`
	RequiredChecksPass       bool             `json:"required_checks_pass"`
	ScopePass                bool             `json:"scope_pass"`
	SourceStableDuringChecks bool             `json:"source_stable_during_checks"`
	ScopeViolationCount      int              `json:"scope_violation_count"`
	Checks                   []ExportCheck    `json:"checks"`
	CompletionRecord         ExportCompletion `json:"completion_record"`
}

type ExportCheck struct {
	Index           int          `json:"index"`
	Required        bool         `json:"required"`
	Passed          bool         `json:"passed"`
	TimedOut        bool         `json:"timed_out"`
	ExitCode        *json.Number `json:"exit_code"`
	DurationSeconds *float64     `json:"duration_seconds"`
}

type ExportCompletion struct {
	State       string  `json:"state"`
	CompletedAt *string `json:"completed_at"`
}

type ExportIssue struct {
	Code   string  `json:"code"`
	TaskID *string `json:"task_id"`
	RunID  *string `json:"run_id"`
}

// ExportRuns enumerates exact stored identities without choosing a latest Run,
// executing checks, observing current source, or writing repository state.
func ExportRuns(cwd, version string) (*ExportReport, error) {
	return exportRunsWithHooks(cwd, version, exportHooks{})
}

func exportRunsWithHooks(cwd, version string, hooks exportHooks) (*ExportReport, error) {
	repository, err := findRepositoryRoot(cwd)
	if err != nil {
		return nil, err
	}
	report := &ExportReport{
		Schema: "seal-run-export/v1", ExporterVersion: version, ScanComplete: true,
		Tasks: []ExportTask{}, Runs: []ExportRun{}, Issues: []ExportIssue{},
	}
	root, err := os.OpenRoot(repository)
	if err != nil {
		return nil, &RepositoryError{message: "Could not open the repository for Run export."}
	}
	defer root.Close()
	before, beforeCode := exportInventory(root)
	if beforeCode != "" {
		report.issue(beforeCode, "", "")
	}
	defer func() {
		if hooks.afterScan != nil {
			hooks.afterScan()
		}
		after, afterCode := exportInventory(root)
		if afterCode != "" && afterCode != beforeCode {
			report.issue(afterCode, "", "")
		}
		if beforeCode != afterCode || !sameExportInventory(before, after) {
			report.issue("concurrent_change", "", "")
		}
	}()
	seal, code := openExportDirectory(root, ".seal")
	if code == "absent" {
		return report, nil
	}
	if code != "" {
		report.issue(code, "", "")
		return report, nil
	}
	defer seal.Close()
	report.exportTasks(repository, seal)
	evidence, code := openExportDirectory(seal, "evidence")
	if code == "absent" {
		return report, nil
	}
	if code != "" {
		report.issue(code, "", "")
		return report, nil
	}
	defer evidence.Close()
	tasks, code := exportEntries(evidence)
	if code != "" {
		report.issue(code, "", "")
	}
	runCount := 0
	for _, entry := range tasks {
		taskID := entry.Name()
		if validateIdentity(taskID, "Task") != nil {
			report.issue("invalid_identity", "", "")
			continue
		}
		parent, code := openExportDirectory(evidence, taskID)
		if code != "" {
			report.issue(exportMissingCode(code), taskID, "")
			continue
		}
		runs, code := exportEntries(parent)
		if code != "" {
			report.issue(code, taskID, "")
		}
		for _, entry := range runs {
			runID := entry.Name()
			if strings.HasPrefix(runID, ".tmp-") {
				continue // Verify publishes these private staging directories later.
			}
			if runCount == exportEntryLimit {
				report.issue("scan_limit", taskID, "")
				parent.Close()
				return report, nil
			}
			runCount++
			if validateIdentity(runID, "Run") != nil {
				report.issue("invalid_identity", taskID, "")
				continue
			}
			runRoot, code := openExportDirectory(parent, runID)
			if code != "" {
				report.issue(exportMissingCode(code), taskID, runID)
				continue
			}
			runRoot.Close()
			if code := exportTaskSafety(seal, taskID); code != "" {
				report.issue(code, taskID, runID)
				continue
			}
			validated, err := ValidateRun(repository, taskID, runID)
			if err != nil {
				report.issue("invalid_evidence", taskID, runID)
				continue
			}
			report.Runs = append(report.Runs, report.projectRun(validated))
		}
		parent.Close()
	}
	return report, nil
}

func (report *ExportReport) exportTasks(repository string, seal *os.Root) {
	tasks, code := openExportDirectory(seal, "tasks")
	if code == "absent" {
		return
	}
	if code != "" {
		report.issue(code, "", "")
		return
	}
	defer tasks.Close()
	entries, code := exportEntries(tasks)
	if code != "" {
		report.issue(code, "", "")
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, ".task.tmp-") {
			// Task publication may leave its private temporary link after commit.
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			report.issue("invalid_identity", "", "")
			continue
		}
		taskID := strings.TrimSuffix(name, ".json")
		if validateIdentity(taskID, "Task") != nil {
			report.issue("invalid_identity", "", "")
			continue
		}
		if code := exportTaskSafety(seal, taskID); code != "" {
			report.issue(code, taskID, "")
			continue
		}
		task, err := readSavedTask(repository, taskID)
		if err != nil {
			report.issue("invalid_task", taskID, "")
			continue
		}
		if err := validateTaskSnapshot(task, taskID, "saved Task snapshot"); err != nil {
			report.issue("invalid_task", taskID, "")
			continue
		}
		report.Tasks = append(report.Tasks, ExportTask{TaskID: taskID})
	}
	sort.Slice(report.Tasks, func(i, j int) bool { return report.Tasks[i].TaskID < report.Tasks[j].TaskID })
}

// The export is not a transactional filesystem snapshot. Comparing the
// published names and object identities under .seal/tasks and .seal/evidence
// before and after the scan detects concurrent Task, Run, and Completion
// publication, removal, and replacement. File contents are not read, so an
// in-place write to an existing document is not detected.
func exportInventory(root *os.Root) (map[string]fs.FileInfo, string) {
	inventory := map[string]fs.FileInfo{}
	seal, code := openExportDirectory(root, ".seal")
	if code == "absent" {
		return inventory, ""
	}
	if code != "" {
		return inventory, code
	}
	defer seal.Close()
	info, err := seal.Stat(".")
	if err != nil {
		return inventory, "unreadable"
	}
	inventory[".seal"] = info
	// Task snapshots are one level deep; Evidence is Task, Run, then Run files.
	for _, directory := range []struct {
		name   string
		levels int
	}{{"tasks", 0}, {"evidence", 2}} {
		if code := listExportInventory(inventory, seal, directory.name, ".seal/"+directory.name, directory.levels); code != "" {
			return inventory, code
		}
	}
	return inventory, ""
}

func listExportInventory(inventory map[string]fs.FileInfo, parent *os.Root, name, path string, levels int) string {
	directory, code := openExportDirectory(parent, name)
	if code == "absent" {
		return ""
	}
	if code != "" {
		return code
	}
	defer directory.Close()
	info, err := directory.Stat(".")
	if err != nil {
		return "unreadable"
	}
	inventory[path] = info
	entries, code := exportEntries(directory)
	if code != "" {
		return code
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		if len(inventory) >= exportInventoryEntryLimit {
			return "scan_limit"
		}
		childPath := path + "/" + entry.Name()
		info, err := directory.Lstat(entry.Name())
		if err != nil {
			return "concurrent_change"
		}
		if levels > 0 && info.IsDir() {
			if code := listExportInventory(inventory, directory, entry.Name(), childPath, levels-1); code != "" {
				return code
			}
			continue
		}
		inventory[childPath] = info
	}
	return ""
}

func sameExportInventory(a, b map[string]fs.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for path, before := range a {
		after, ok := b[path]
		if !ok || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			return false
		}
	}
	return true
}

func (report *ExportReport) issue(code, taskID, runID string) {
	issue := ExportIssue{Code: code}
	if taskID != "" {
		issue.TaskID = &taskID
	}
	if runID != "" {
		issue.RunID = &runID
	}
	report.ScanComplete = false
	report.Issues = append(report.Issues, issue)
}

func openExportDirectory(parent *os.Root, name string) (*os.Root, string) {
	info, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "absent"
	}
	if err != nil {
		return nil, "unreadable"
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "unsafe_entry"
	}
	opened, err := parent.OpenRoot(name)
	if err != nil {
		return nil, "unreadable"
	}
	actual, err := opened.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		opened.Close()
		return nil, "unsafe_entry"
	}
	return opened, ""
}

func exportEntries(root *os.Root) ([]os.DirEntry, string) {
	file, err := root.Open(".")
	if err != nil {
		return nil, "unreadable"
	}
	defer file.Close()
	entries, err := file.ReadDir(exportEntryLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "unreadable"
	}
	// Never return an arbitrary directory-order subset when the bound is hit.
	if len(entries) > exportEntryLimit {
		return nil, "scan_limit"
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, ""
}

func exportTaskSafety(seal *os.Root, taskID string) string {
	tasks, code := openExportDirectory(seal, "tasks")
	if code != "" {
		return exportMissingCode(code)
	}
	defer tasks.Close()
	info, err := tasks.Lstat(taskID + ".json")
	if err != nil {
		return "unreadable"
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "unsafe_entry"
	}
	return ""
}

func exportMissingCode(code string) string {
	if code == "absent" {
		return "unreadable"
	}
	return code
}

func (report *ExportReport) projectRun(run *ValidatedRun) ExportRun {
	result := ExportRun{
		TaskID: run.taskID, RunID: run.runID, EvidenceSHA256: run.evidenceSHA256,
		MechanicalResult: run.mechanicalResult, RequiredChecksPass: run.requiredChecksPass,
		ScopePass: run.scopePass, SourceStableDuringChecks: run.sourceStableDuringChecks,
		ScopeViolationCount: len(run.scopeViolations), Checks: make([]ExportCheck, len(run.checks)),
		CompletionRecord: ExportCompletion{State: "absent"},
	}
	if _, err := time.Parse(time.RFC3339Nano, run.timestamp); err == nil {
		timestamp := run.timestamp
		result.Timestamp = &timestamp
	} else {
		report.issue("invalid_metric", run.taskID, run.runID)
	}
	for index, check := range run.checks {
		required := pyjson.Equal(check.Required, true)
		duration := finiteDuration(run.checkDurations[index])
		if duration == nil {
			report.issue("invalid_metric", run.taskID, run.runID)
		}
		result.Checks[index] = ExportCheck{
			Index: index, Required: required, Passed: check.Passed, TimedOut: check.TimedOut,
			ExitCode: check.ExitCode, DurationSeconds: duration,
		}
	}
	store, err := openCompletionStore(run, completeHooks{})
	if err != nil {
		result.CompletionRecord.State = "invalid"
		report.issue("invalid_completion", run.taskID, run.runID)
		return result
	}
	defer store.close()
	contents, err := store.readExistingBounded(run, 64<<10)
	if err != nil {
		result.CompletionRecord.State = "invalid"
		report.issue("invalid_completion", run.taskID, run.runID)
		return result
	}
	if contents == nil {
		return result
	}
	if run.verifierRequired || run.mechanicalResult != "pass" {
		result.CompletionRecord.State = "invalid"
		report.issue("invalid_completion", run.taskID, run.runID)
		return result
	}
	document, _ := decodeCompletionObject(contents) // readExistingBounded validated this exact record.
	completedAt := document["completed_at"].(string)
	result.CompletionRecord = ExportCompletion{State: "recorded_pass", CompletedAt: &completedAt}
	return result
}

func finiteDuration(value any) *float64 {
	var duration float64
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(number), 64)
		if err != nil {
			return nil
		}
		duration = parsed
	case pyjson.Float:
		duration = float64(number)
	default:
		return nil
	}
	if duration < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return nil
	}
	return &duration
}
