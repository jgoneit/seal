package runstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var requiredEvidenceFiles = []string{
	"task.json",
	"changed-files.json",
	"diff.patch",
	"checks.json",
	"verification.json",
	"source-before-checks.json",
	"source-after-checks.json",
}

type validatedDocuments struct {
	expectedFiles            []string
	checkSummaries           []Check
	scopeViolations          []ScopeViolation
	baseline                 string
	verifierRequired         bool
	sourceBeforeSHA256       string
	sourceAfterSHA256        string
	scopePass                bool
	requiredChecksPass       bool
	sourceStableDuringChecks bool
	mechanicalResult         string
}

type taskFacts struct {
	baseline         string
	scope            []string
	checks           []taskCheck
	verifierRequired bool
}

type taskCheck struct {
	raw      map[string]any
	required bool
	timeout  any
}

type verificationFacts struct {
	document jsonObject
	files    map[string]string
}

type changedFacts struct {
	productChanges []any
	violations     []any
	projected      []ScopeViolation
	scopePass      bool
}

func validateTaskSnapshot(task jsonObject, taskID, context string) error {
	_, err := parseTaskFacts(task, taskID, context)
	return err
}

func parseTaskFacts(task jsonObject, taskID, context string) (taskFacts, error) {
	if !integerEquals(task["schema_version"], taskSchemaVersion) {
		return taskFacts{}, &IdentityError{message: context + " has an unsupported schema_version."}
	}
	if id, ok := task["id"].(string); !ok || id != taskID {
		return taskFacts{}, &IdentityError{message: fmt.Sprintf(
			"%s id does not match requested Task id '%s'.",
			context,
			taskID,
		)}
	}
	baseline, ok := task["baseline"].(string)
	if !ok || baseline == "" {
		return taskFacts{}, &IdentityError{message: context + " baseline must be a non-empty string."}
	}

	rawScope, ok := task["scope"].([]any)
	if !ok || len(rawScope) == 0 {
		return taskFacts{}, &IdentityError{message: context + " scope must be a non-empty array."}
	}
	scope := make([]string, len(rawScope))
	for index, value := range rawScope {
		path, err := safeRepositoryPath(value, fmt.Sprintf("%s scope[%d]", context, index), true)
		if err != nil {
			return taskFacts{}, err
		}
		scope[index] = path
	}

	rawChecks, ok := task["checks"].([]any)
	if !ok || len(rawChecks) == 0 {
		return taskFacts{}, &IdentityError{message: context + " checks must be a non-empty array."}
	}
	checks := make([]taskCheck, len(rawChecks))
	for index, value := range rawChecks {
		check, ok := value.(map[string]any)
		if !ok {
			return taskFacts{}, &IdentityError{message: fmt.Sprintf("%s checks[%d] must be an object.", context, index)}
		}
		name, ok := check["name"].(string)
		if !ok || name == "" {
			return taskFacts{}, &IdentityError{message: fmt.Sprintf("%s checks[%d].name must be a non-empty string.", context, index)}
		}
		argv, ok := check["argv"].([]any)
		if !ok || len(argv) == 0 {
			return taskFacts{}, &IdentityError{message: fmt.Sprintf("%s checks[%d].argv must be a non-empty string array.", context, index)}
		}
		for _, argument := range argv {
			text, ok := argument.(string)
			if !ok || text == "" {
				return taskFacts{}, &IdentityError{message: fmt.Sprintf("%s checks[%d].argv must be a non-empty string array.", context, index)}
			}
		}
		required, ok := check["required"].(bool)
		if !ok {
			return taskFacts{}, &IdentityError{message: fmt.Sprintf("%s checks[%d].required must be a boolean.", context, index)}
		}
		timeout, present := check["timeout_seconds"]
		if !present {
			timeout = json.Number("300")
		}
		if !positiveInteger(timeout) {
			return taskFacts{}, &IdentityError{message: fmt.Sprintf("%s checks[%d].timeout_seconds must be a positive integer.", context, index)}
		}
		checks[index] = taskCheck{raw: check, required: required, timeout: timeout}
	}

	verifier, ok := task["verifier"].(map[string]any)
	if !ok {
		return taskFacts{}, &IdentityError{message: context + " verifier must contain a required boolean."}
	}
	verifierRequired, ok := verifier["required"].(bool)
	if !ok {
		return taskFacts{}, &IdentityError{message: context + " verifier must contain a required boolean."}
	}
	return taskFacts{
		baseline:         baseline,
		scope:            scope,
		checks:           checks,
		verifierRequired: verifierRequired,
	}, nil
}

func validateDocuments(runDirectory string, task jsonObject, taskID, runID string) (validatedDocuments, error) {
	return validateDocumentsContext(context.Background(), runDirectory, task, taskID, runID)
}

func validateDocumentsContext(ctx context.Context, runDirectory string, task jsonObject, taskID, runID string) (validatedDocuments, error) {
	if err := artifactContextError(ctx); err != nil {
		return validatedDocuments{}, err
	}
	evidenceTask, err := readEvidenceJSONContext(ctx, runDirectory, "task.json")
	if err != nil {
		return validatedDocuments{}, err
	}
	changedDocument, err := readEvidenceJSONContext(ctx, runDirectory, "changed-files.json")
	if err != nil {
		return validatedDocuments{}, err
	}
	checksDocument, err := readEvidenceJSONContext(ctx, runDirectory, "checks.json")
	if err != nil {
		return validatedDocuments{}, err
	}
	verificationDocument, err := readEvidenceJSONContext(ctx, runDirectory, "verification.json")
	if err != nil {
		return validatedDocuments{}, err
	}
	if _, err := readRequiredArtifactContext(ctx, runDirectory, "diff.patch"); err != nil {
		return validatedDocuments{}, err
	}

	if err := validateTaskSnapshot(evidenceTask, taskID, "task.json"); err != nil {
		return validatedDocuments{}, err
	}
	if !jsonEqual(map[string]any(evidenceTask), map[string]any(task)) {
		return validatedDocuments{}, &IdentityError{message: fmt.Sprintf(
			"task.json does not match saved Task snapshot '%s'.",
			taskID,
		)}
	}
	taskDefinition, err := parseTaskFacts(task, taskID, "saved Task snapshot")
	if err != nil {
		return validatedDocuments{}, err
	}

	if err := artifactContextError(ctx); err != nil {
		return validatedDocuments{}, err
	}
	verification, err := validateVerification(ctx, runDirectory, verificationDocument, taskID, runID)
	if err != nil {
		return validatedDocuments{}, err
	}
	checkSummaries, requiredChecksPass, logPaths, err := validateChecks(
		ctx,
		runDirectory,
		taskDefinition,
		checksDocument,
		verification.files,
	)
	if err != nil {
		return validatedDocuments{}, err
	}
	changed, err := validateChangedFiles(taskDefinition, changedDocument, verification.document)
	if err != nil {
		return validatedDocuments{}, err
	}
	sourceStable, err := validateSourceBindingContext(
		ctx,
		runDirectory,
		taskDefinition.baseline,
		changedDocument,
		verification.document,
	)
	if err != nil {
		return validatedDocuments{}, err
	}

	mechanicalResult := "fail"
	if changed.scopePass && requiredChecksPass && sourceStable {
		mechanicalResult = "pass"
	}
	if !jsonEqual(verification.document["required_checks_pass"], requiredChecksPass) {
		return validatedDocuments{}, &EvidenceError{message: "verification.json required_checks_pass does not match checks.json."}
	}
	if verification.document["mechanical_result"] != mechanicalResult {
		return validatedDocuments{}, &EvidenceError{message: "verification.json mechanical_result does not match saved scope and checks."}
	}

	expectedFiles := append([]string(nil), requiredEvidenceFiles...)
	expectedFiles = append(expectedFiles, logPaths...)
	sort.Strings(expectedFiles)
	if err := validateExactEvidenceList(verification.files, expectedFiles); err != nil {
		return validatedDocuments{}, err
	}
	return validatedDocuments{
		expectedFiles:            expectedFiles,
		checkSummaries:           checkSummaries,
		scopeViolations:          changed.projected,
		baseline:                 taskDefinition.baseline,
		verifierRequired:         taskDefinition.verifierRequired,
		sourceBeforeSHA256:       verification.document["source_before_checks_sha256"].(string),
		sourceAfterSHA256:        verification.document["source_after_checks_sha256"].(string),
		scopePass:                changed.scopePass,
		requiredChecksPass:       requiredChecksPass,
		sourceStableDuringChecks: sourceStable,
		mechanicalResult:         mechanicalResult,
	}, nil
}

func readEvidenceJSON(runDirectory, relativePath string) (jsonObject, error) {
	return readEvidenceJSONContext(context.Background(), runDirectory, relativePath)
}

func readEvidenceJSONContext(ctx context.Context, runDirectory, relativePath string) (jsonObject, error) {
	contents, err := readRequiredArtifactContext(ctx, runDirectory, relativePath)
	if err != nil {
		return nil, err
	}
	value, err := decodeJSONObject(contents)
	if err != nil {
		if relativePath == "task.json" && isStandardJSONDepthLimit(err) {
			return nil, &RuntimeError{message: "Evidence file 'task.json' exceeds the supported JSON nesting depth."}
		}
		if KindOf(err) == KindRuntime {
			return nil, err
		}
		return nil, &EvidenceError{message: fmt.Sprintf("Evidence file '%s' is not valid JSON.", relativePath)}
	}
	return value, nil
}

func validateVerification(ctx context.Context, runDirectory string, document jsonObject, taskID, runID string) (verificationFacts, error) {
	if err := artifactContextError(ctx); err != nil {
		return verificationFacts{}, err
	}
	if !integerEquals(document["schema_version"], evidenceSchemaVersion) {
		return verificationFacts{}, &EvidenceError{message: "verification.json has an unsupported schema_version."}
	}
	if !exactKeys(map[string]any(document),
		"schema_version", "task_id", "run_id", "baseline", "changed_files",
		"scope_pass", "scope_violations", "required_checks_pass", "mechanical_result",
		"evidence_files", "timestamp", "duration", "source_snapshot_schema_version",
		"source_before_checks_sha256", "source_after_checks_sha256", "source_stable_during_checks",
	) {
		return verificationFacts{}, &EvidenceError{message: "verification.json has missing or unexpected field(s)."}
	}
	for _, field := range []string{"task_id", "run_id", "baseline", "timestamp"} {
		if value, ok := document[field].(string); !ok || value == "" {
			return verificationFacts{}, &EvidenceError{message: fmt.Sprintf("verification.json %s must be a non-empty string.", field)}
		}
	}
	for _, field := range []string{"changed_files", "scope_violations", "evidence_files"} {
		if _, ok := document[field].([]any); !ok {
			return verificationFacts{}, &EvidenceError{message: fmt.Sprintf("verification.json %s must be an array.", field)}
		}
	}
	for _, field := range []string{"scope_pass", "required_checks_pass"} {
		if _, ok := document[field].(bool); !ok {
			return verificationFacts{}, &EvidenceError{message: fmt.Sprintf("verification.json %s must be a boolean.", field)}
		}
	}
	mechanical, ok := document["mechanical_result"].(string)
	if !ok || mechanical != "pass" && mechanical != "fail" {
		return verificationFacts{}, &EvidenceError{message: "verification.json mechanical_result must be 'pass' or 'fail'."}
	}
	if !nonNegativeNumber(document["duration"]) {
		return verificationFacts{}, &EvidenceError{message: "verification.json duration must be a non-negative number."}
	}
	if document["task_id"] != taskID {
		return verificationFacts{}, &IdentityError{message: "verification.json task_id does not match the requested Task id."}
	}
	if document["run_id"] != runID {
		return verificationFacts{}, &IdentityError{message: "verification.json run_id does not match the requested run id."}
	}

	rawFiles := document["evidence_files"].([]any)
	if len(rawFiles) == 0 {
		return verificationFacts{}, &EvidenceError{message: "verification.json evidence_files must be a non-empty array."}
	}
	files := make(map[string]string, len(rawFiles))
	for index, value := range rawFiles {
		if err := artifactContextError(ctx); err != nil {
			return verificationFacts{}, err
		}
		path, err := safeRunPath(value, fmt.Sprintf("verification.json evidence_files[%d]", index))
		if err != nil {
			return verificationFacts{}, err
		}
		if _, exists := files[path]; exists {
			return verificationFacts{}, &EvidenceError{message: fmt.Sprintf("verification.json evidence_files[%d] duplicates '%s'.", index, path)}
		}
		files[path] = path
		if _, err := readRequiredArtifactContext(ctx, runDirectory, path); err != nil {
			return verificationFacts{}, err
		}
	}
	missing := make([]string, 0)
	for _, required := range requiredEvidenceFiles {
		if _, ok := files[required]; !ok {
			missing = append(missing, required)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return verificationFacts{}, &EvidenceError{message: "verification.json evidence_files is missing required entry(s): " + strings.Join(missing, ", ") + "."}
	}
	return verificationFacts{document: document, files: files}, nil
}

func validateChecks(
	ctx context.Context,
	runDirectory string,
	task taskFacts,
	document jsonObject,
	evidenceFiles map[string]string,
) ([]Check, bool, []string, error) {
	if !exactKeys(map[string]any(document), "schema_version", "checks") {
		return nil, false, nil, &EvidenceError{message: "checks.json has missing or unexpected field(s)."}
	}
	if !integerEquals(document["schema_version"], 1) {
		return nil, false, nil, &EvidenceError{message: "checks.json has an unsupported schema_version."}
	}
	recorded, ok := document["checks"].([]any)
	if !ok {
		return nil, false, nil, &EvidenceError{message: "checks.json checks must be an array."}
	}
	if len(recorded) != len(task.checks) {
		return nil, false, nil, &EvidenceError{message: "checks.json does not contain one result for every Task check."}
	}

	seen := make(map[string]struct{}, len(requiredEvidenceFiles)+2*len(recorded))
	for _, path := range requiredEvidenceFiles {
		seen[path] = struct{}{}
	}
	summaries := make([]Check, len(recorded))
	logPaths := make([]string, 0, 2*len(recorded))
	requiredPass := true
	for index, rawRecord := range recorded {
		if err := artifactContextError(ctx); err != nil {
			return nil, false, nil, err
		}
		context := fmt.Sprintf("checks.json checks[%d]", index)
		record, ok := rawRecord.(map[string]any)
		if !ok {
			return nil, false, nil, &EvidenceError{message: context + " must be an object."}
		}
		if !exactKeys(record,
			"name", "argv", "cwd", "started_at", "finished_at", "duration_seconds",
			"effective_timeout", "exit_code", "timed_out", "stdout_path", "stderr_path",
			"required", "passed",
		) {
			return nil, false, nil, &EvidenceError{message: context + " has missing or unexpected field(s)."}
		}
		for _, field := range []string{"name", "argv", "required"} {
			if !jsonEqual(record[field], task.checks[index].raw[field]) {
				return nil, false, nil, &EvidenceError{message: fmt.Sprintf("%s.%s does not match saved Task check[%d].%s.", context, field, index, field)}
			}
		}
		for _, field := range []string{"cwd", "started_at", "finished_at"} {
			if text, ok := record[field].(string); !ok || text == "" {
				return nil, false, nil, &EvidenceError{message: fmt.Sprintf("%s.%s must be a non-empty string.", context, field)}
			}
		}
		if !nonNegativeNumber(record["duration_seconds"]) {
			return nil, false, nil, &EvidenceError{message: context + ".duration_seconds must be a non-negative number."}
		}
		if !jsonEqual(record["effective_timeout"], task.checks[index].timeout) {
			return nil, false, nil, &EvidenceError{message: fmt.Sprintf("%s.effective_timeout does not match saved Task check[%d].", context, index)}
		}
		passed, passedOK := record["passed"].(bool)
		timedOut, timedOutOK := record["timed_out"].(bool)
		if !passedOK {
			return nil, false, nil, &EvidenceError{message: context + ".passed must be a boolean."}
		}
		if !timedOutOK {
			return nil, false, nil, &EvidenceError{message: context + ".timed_out must be a boolean."}
		}
		var exitCode *json.Number
		if record["exit_code"] != nil {
			number, ok := isJSONInteger(record["exit_code"])
			if !ok {
				return nil, false, nil, &EvidenceError{message: context + ".exit_code must be an integer or null."}
			}
			exitCode = &number
		}
		exitZero := exitCode != nil && integerEquals(*exitCode, 0)
		if passed != (!timedOut && exitZero) {
			return nil, false, nil, &EvidenceError{message: context + ".passed does not match timed_out and exit_code."}
		}

		for _, field := range []string{"stdout_path", "stderr_path"} {
			path, err := safeRunPath(record[field], context+"."+field)
			if err != nil {
				return nil, false, nil, err
			}
			if _, ok := evidenceFiles[path]; !ok {
				return nil, false, nil, &EvidenceError{message: context + "." + field + " is not listed in verification.json evidence_files."}
			}
			if _, duplicate := seen[path]; duplicate {
				return nil, false, nil, &EvidenceError{message: context + "." + field + " duplicates an existing evidence path '" + path + "'."}
			}
			seen[path] = struct{}{}
			if _, err := readRequiredArtifactContext(ctx, runDirectory, path); err != nil {
				return nil, false, nil, err
			}
			logPaths = append(logPaths, path)
		}
		name := record["name"].(string)
		summaries[index] = Check{
			ExitCode: exitCode,
			Name:     name,
			Passed:   passed,
			Required: cloneJSON(record["required"]),
			TimedOut: timedOut,
		}
		if task.checks[index].required && !passed {
			requiredPass = false
		}
	}
	if err := artifactContextError(ctx); err != nil {
		return nil, false, nil, err
	}
	return summaries, requiredPass, logPaths, nil
}

func validateChangedFiles(task taskFacts, document, verification jsonObject) (changedFacts, error) {
	if !exactKeys(map[string]any(document), "schema_version", "baseline", "scope", "changes") {
		return changedFacts{}, &EvidenceError{message: "changed-files.json has missing or unexpected field(s)."}
	}
	if !integerEquals(document["schema_version"], 1) {
		return changedFacts{}, &EvidenceError{message: "changed-files.json has an unsupported schema_version."}
	}
	baseline, ok := document["baseline"].(string)
	if !ok || baseline == "" {
		return changedFacts{}, &EvidenceError{message: "changed-files.json baseline must be a non-empty string."}
	}
	if baseline != verification["baseline"] {
		return changedFacts{}, &EvidenceError{message: "changed-files.json baseline does not match verification.json baseline."}
	}
	rawScope, ok := document["scope"].([]any)
	if !ok {
		return changedFacts{}, &EvidenceError{message: "changed-files.json scope must be an array."}
	}
	recordedScope := make([]string, len(rawScope))
	for index, value := range rawScope {
		path, err := safeRepositoryPath(value, fmt.Sprintf("changed-files.json scope[%d]", index), true)
		if err != nil {
			return changedFacts{}, err
		}
		recordedScope[index] = path
	}
	if len(recordedScope) != len(task.scope) {
		return changedFacts{}, &EvidenceError{message: "changed-files.json scope does not match saved Task scope."}
	}
	for index := range recordedScope {
		if recordedScope[index] != task.scope[index] {
			return changedFacts{}, &EvidenceError{message: "changed-files.json scope does not match saved Task scope."}
		}
	}
	rawChanges, ok := document["changes"].([]any)
	if !ok {
		return changedFacts{}, &EvidenceError{message: "changed-files.json changes must be an array."}
	}
	productChanges := make([]any, 0, len(rawChanges))
	violations := make([]any, 0)
	projected := make([]ScopeViolation, 0)
	for index, value := range rawChanges {
		change, inScope, metadata, projection, err := validateChange(value, fmt.Sprintf("changed-files.json changes[%d]", index), task.scope)
		if err != nil {
			return changedFacts{}, err
		}
		if metadata {
			continue
		}
		productChanges = append(productChanges, change)
		if !inScope {
			violations = append(violations, change)
			projected = append(projected, projection)
		}
	}
	if !jsonEqual(verification["changed_files"], productChanges) {
		return changedFacts{}, &EvidenceError{message: "verification.json changed_files does not match product changes in changed-files.json."}
	}
	if !jsonEqual(verification["scope_violations"], violations) {
		return changedFacts{}, &EvidenceError{message: "verification.json scope_violations does not match out-of-scope product changes."}
	}
	scopePass := len(violations) == 0
	if !jsonEqual(verification["scope_pass"], scopePass) {
		return changedFacts{}, &EvidenceError{message: "verification.json scope_pass does not match scope_violations."}
	}
	return changedFacts{
		productChanges: productChanges,
		violations:     violations,
		projected:      projected,
		scopePass:      scopePass,
	}, nil
}

func validateChange(value any, context string, scope []string) (map[string]any, bool, bool, ScopeViolation, error) {
	change, ok := value.(map[string]any)
	if !ok {
		return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + " must be an object."}
	}
	if !exactKeys(change,
		"source", "status", "path", "previous_path", "old_mode", "new_mode",
		"mode_changed", "is_binary", "in_scope",
	) {
		return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + " has missing or unexpected field(s)."}
	}
	source, ok := change["source"].(string)
	if !ok || source != "committed" && source != "staged" && source != "unstaged" && source != "untracked" {
		return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + ".source is invalid."}
	}
	status, ok := change["status"].(string)
	if !ok || status == "" {
		return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + ".status must be a non-empty string."}
	}
	path, err := safeRepositoryPath(change["path"], context+".path", false)
	if err != nil {
		return nil, false, false, ScopeViolation{}, err
	}
	var previous *string
	if change["previous_path"] != nil {
		previousPath, err := safeRepositoryPath(change["previous_path"], context+".previous_path", false)
		if err != nil {
			return nil, false, false, ScopeViolation{}, err
		}
		previous = &previousPath
	}
	for _, field := range []string{"old_mode", "new_mode"} {
		if change[field] == nil {
			continue
		}
		if text, ok := change[field].(string); !ok || text == "" {
			return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + "." + field + " must be a non-empty string or null."}
		}
	}
	for _, field := range []string{"mode_changed", "is_binary", "in_scope"} {
		if _, ok := change[field].(bool); !ok {
			return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + "." + field + " must be a boolean."}
		}
	}
	inScope := changeWithinScope(status, path, previous, scope)
	if change["in_scope"] != inScope {
		return nil, false, false, ScopeViolation{}, &EvidenceError{message: context + ".in_scope does not match saved Task scope."}
	}
	metadata := isMetadataPath(path) && (previous == nil || isMetadataPath(*previous))
	return change, inScope, metadata, ScopeViolation{
		Path:         path,
		PreviousPath: previous,
		Source:       source,
		Status:       status,
	}, nil
}

func safeRepositoryPath(value any, context string, allowDot bool) (string, error) {
	path, ok := value.(string)
	if !ok || path == "" {
		return "", &EvidenceError{message: context + " must be a non-empty repository-relative path."}
	}
	if allowDot && path == "." {
		return path, nil
	}
	if strings.Contains(path, `\`) || strings.ContainsRune(path, 0) {
		return "", &EvidenceError{message: context + " must use a repository-relative POSIX path."}
	}
	if strings.HasPrefix(path, "/") || len(path) >= 2 && path[1] == ':' {
		return "", &EvidenceError{message: context + " must be a repository-relative path."}
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", &EvidenceError{message: context + " must be a repository-relative path."}
		}
	}
	return path, nil
}

func changeWithinScope(status, path string, previous *string, scope []string) bool {
	paths := []string{path}
	if status == "renamed" && previous != nil {
		paths = append(paths, *previous)
	}
	for _, changedPath := range paths {
		inside := false
		for _, boundary := range scope {
			if boundary == "." || changedPath == boundary || strings.HasPrefix(changedPath, boundary+"/") {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	return true
}

func isMetadataPath(path string) bool {
	switch path {
	case ".seal/runs.jsonl", ".seal/lessons.md", ".seal/config.json":
		return true
	}
	return path == ".seal/tasks" || strings.HasPrefix(path, ".seal/tasks/") ||
		path == ".seal/evidence" || strings.HasPrefix(path, ".seal/evidence/")
}

func validateExactEvidenceList(listed map[string]string, expected []string) error {
	expectedSet := make(map[string]struct{}, len(expected))
	for _, path := range expected {
		expectedSet[path] = struct{}{}
	}
	missing := make([]string, 0)
	for path := range expectedSet {
		if _, ok := listed[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return &EvidenceError{message: "verification.json evidence_files is missing expected mechanical entry(s): " + strings.Join(missing, ", ") + "."}
	}
	unexpected := make([]string, 0)
	for path := range listed {
		if _, ok := expectedSet[path]; !ok {
			unexpected = append(unexpected, path)
		}
	}
	if len(unexpected) != 0 {
		sort.Strings(unexpected)
		return &EvidenceError{message: "verification.json evidence_files has unexpected non-mechanical entry(s): " + strings.Join(unexpected, ", ") + "."}
	}
	return nil
}
