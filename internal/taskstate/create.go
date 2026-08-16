package taskstate

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const taskSchemaVersion = 1

var taskTypes = map[string]struct{}{
	"bugfix":       {},
	"config-infra": {},
	"docs":         {},
	"feature":      {},
	"refactor":     {},
	"test":         {},
}

var taskRisks = map[string]struct{}{
	"high":   {},
	"low":    {},
	"medium": {},
}

// Create validates one Task Spec, resolves its catalog checks and current Git
// baseline, and stores the normalized snapshot through the Task writer's
// publication boundary. The returned bytes are exactly the stored,
// newline-terminated JSON bytes.
func Create(cwd, taskFile string, force bool) ([]byte, error) {
	repository, err := findRepositoryRoot(cwd)
	if err != nil {
		return nil, err
	}

	inputPath := taskInputPath(cwd, taskFile)
	taskSpec, err := loadCreateJSONObject(inputPath, "Task Spec")
	if err != nil {
		return nil, err
	}

	catalog, catalogPath, err := loadCheckCatalog(repository)
	if err != nil {
		return nil, err
	}
	snapshot, err := normalizeTaskSpec(taskSpec, catalog)
	if err != nil {
		return nil, err
	}

	baseline, err := currentHead(repository)
	if err != nil {
		return nil, err
	}
	snapshot["baseline"] = baseline

	document := Document{values: snapshot, taskID: snapshot["id"].(string)}
	encoded, err := Render(document)
	if err != nil {
		return nil, invalidInput("Could not render normalized Task snapshot.", err)
	}
	contents := append(encoded, '\n')
	if err := writeTaskSnapshot(
		repository,
		snapshot["id"].(string),
		contents,
		force,
		inputPath,
		catalogPath,
	); err != nil {
		return nil, err
	}
	return contents, nil
}

func taskInputPath(cwd, taskFile string) string {
	if filepath.IsAbs(taskFile) {
		return filepath.Clean(taskFile)
	}
	if cwd == "" {
		cwd = "."
	}
	return filepath.Clean(filepath.Join(cwd, taskFile))
}

func loadCreateJSONObject(path, context string) (map[string]any, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, invalidInput(fmt.Sprintf("%s file does not exist: %s.", context, path), err)
		}
		return nil, invalidInput(fmt.Sprintf("Could not read %s: %s.", context, path), err)
	}
	if !utf8.Valid(contents) {
		return nil, invalidInput(fmt.Sprintf("%s is not valid UTF-8.", context), nil)
	}

	normalized, markers := replacePythonConstants(contents)
	value, err := decodeJSONValue(normalized)
	if err != nil {
		return nil, invalidInput(fmt.Sprintf("%s is not valid JSON: %v.", context, err), err)
	}
	if containsOversizedPythonInteger(normalized) {
		return nil, invalidInput(
			fmt.Sprintf("%s contains a JSON integer exceeding the supported 4300-digit limit.", context),
			nil,
		)
	}
	if containsUnpairedJSONSurrogate(contents) {
		return nil, invalidInput(
			fmt.Sprintf("%s contains an unpaired UTF-16 surrogate escape.", context),
			nil,
		)
	}

	object, ok := restorePythonConstants(value, markers).(map[string]any)
	if !ok {
		return nil, invalidInput(fmt.Sprintf("%s must be a JSON object.", context), nil)
	}
	return object, nil
}

func loadCheckCatalog(repository string) (map[string]map[string]any, string, error) {
	path := filepath.Join(repository, ".seal", "checks.json")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, path, invalidInput("Check catalog is missing: .seal/checks.json.", err)
	}

	document, err := loadCreateJSONObject(path, "Check catalog")
	if err != nil {
		return nil, path, err
	}
	unexpected := unexpectedKeys(document, []string{"checks", "schema_version"})
	if len(unexpected) != 0 {
		return nil, path, invalidInput(
			fmt.Sprintf("Check catalog has unexpected field(s): %s.", strings.Join(unexpected, ", ")),
			nil,
		)
	}
	if version, ok := document["schema_version"]; ok && !isExactIntegerValue(version, taskSchemaVersion) {
		return nil, path, invalidInput("Check catalog schema_version must be 1.", nil)
	}
	entries, ok := document["checks"]
	if !ok {
		return nil, path, invalidInput("Check catalog is missing required field: checks.", nil)
	}

	definitions := make([]any, 0)
	switch typed := entries.(type) {
	case []any:
		definitions = typed
	case map[string]any:
		keys := sortedMapKeys(typed)
		definitions = make([]any, 0, len(keys))
		for _, name := range keys {
			definition, ok := typed[name].(map[string]any)
			if !ok {
				return nil, path, invalidInput(
					fmt.Sprintf("Check catalog entry '%s' must be a JSON object.", name),
					nil,
				)
			}
			if embeddedName, exists := definition["name"]; exists && embeddedName != name {
				return nil, path, invalidInput(
					fmt.Sprintf("Check catalog entry '%s' has a different name field.", name),
					nil,
				)
			}
			withName := make(map[string]any, len(definition)+1)
			withName["name"] = name
			for key, value := range definition {
				withName[key] = value
			}
			definitions = append(definitions, withName)
		}
	default:
		return nil, path, invalidInput("Check catalog checks must be an array or object.", nil)
	}

	resolved := make(map[string]map[string]any, len(definitions))
	for index, raw := range definitions {
		definition, err := normalizeCheckDefinition(raw, fmt.Sprintf("Check catalog checks[%d]", index))
		if err != nil {
			return nil, path, err
		}
		name := definition["name"].(string)
		if _, duplicate := resolved[name]; duplicate {
			return nil, path, invalidInput(
				fmt.Sprintf("Check catalog defines '%s' more than once.", name),
				nil,
			)
		}
		resolved[name] = definition
	}
	return resolved, path, nil
}

func normalizeTaskSpec(spec map[string]any, catalog map[string]map[string]any) (map[string]any, error) {
	if err := requireExactKeys(
		spec,
		[]string{"checks", "id", "objective", "risk", "schema_version", "scope", "type", "verifier"},
		nil,
		"Task Spec",
	); err != nil {
		return nil, err
	}
	if !isExactIntegerValue(spec["schema_version"], taskSchemaVersion) {
		return nil, invalidInput("Task Spec schema_version must be 1.", nil)
	}

	taskID, ok := spec["id"].(string)
	if !ok {
		return nil, invalidInput("Task id must be a non-empty string.", nil)
	}
	if err := validateID(taskID); err != nil {
		return nil, err
	}

	taskType, ok := spec["type"].(string)
	if _, allowed := taskTypes[taskType]; !ok || !allowed {
		return nil, invalidInput(
			"Task Spec type must be one of: bugfix, config-infra, docs, feature, refactor, test.",
			nil,
		)
	}
	objective, err := requireNonemptyString(spec["objective"], "Task Spec objective")
	if err != nil {
		return nil, err
	}
	risk, ok := spec["risk"].(string)
	if _, allowed := taskRisks[risk]; !ok || !allowed {
		return nil, invalidInput("Task Spec risk must be one of: high, low, medium.", nil)
	}
	scope, err := normalizeScope(spec["scope"])
	if err != nil {
		return nil, err
	}
	checks, err := normalizeChecks(spec["checks"], catalog)
	if err != nil {
		return nil, err
	}
	verifier, err := normalizeVerifier(spec["verifier"])
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"schema_version": json.Number("1"),
		"id":             taskID,
		"type":           taskType,
		"objective":      objective,
		"scope":          scope,
		"checks":         checks,
		"risk":           risk,
		"verifier":       verifier,
	}, nil
}

func normalizeScope(value any) ([]any, error) {
	entries, ok := value.([]any)
	if !ok || len(entries) == 0 {
		return nil, invalidInput("Task Spec scope must be a non-empty array.", nil)
	}
	normalized := make([]any, 0, len(entries))
	for index, raw := range entries {
		context := fmt.Sprintf("Task Spec scope[%d]", index)
		path, err := requireNonemptyString(raw, context)
		if err != nil {
			return nil, err
		}
		portable := strings.ReplaceAll(path, `\`, "/")
		pathRunes := []rune(path)
		hasWindowsDrive := len(pathRunes) >= 2 && pathRunes[1] == ':'
		if strings.HasPrefix(portable, "/") || hasWindowsDrive {
			return nil, invalidInput(context+" must be relative to the repository root.", nil)
		}
		parts := strings.Split(portable, "/")
		kept := make([]string, 0, len(parts))
		for _, part := range parts {
			if part == ".." {
				return nil, invalidInput(context+" must not contain '..' traversal.", nil)
			}
			if part != "" && part != "." {
				kept = append(kept, part)
			}
		}
		if len(kept) == 0 {
			normalized = append(normalized, ".")
		} else {
			normalized = append(normalized, strings.Join(kept, "/"))
		}
	}
	return normalized, nil
}

func normalizeChecks(value any, catalog map[string]map[string]any) ([]any, error) {
	entries, ok := value.([]any)
	if !ok || len(entries) == 0 {
		return nil, invalidInput("Task Spec checks must be a non-empty array.", nil)
	}
	normalized := make([]any, 0, len(entries))
	for index, raw := range entries {
		context := fmt.Sprintf("Task Spec checks[%d]", index)
		if name, ok := raw.(string); ok {
			if name == "" {
				return nil, invalidInput(context+" must be a non-empty string.", nil)
			}
			definition, exists := catalog[name]
			if !exists {
				return nil, invalidInput(
					fmt.Sprintf("%s references unknown catalog check '%s'.", context, name),
					nil,
				)
			}
			normalized = append(normalized, cloneCheckDefinition(definition))
			continue
		}
		definition, err := normalizeCheckDefinition(raw, context)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, definition)
	}
	return normalized, nil
}

func normalizeCheckDefinition(value any, context string) (map[string]any, error) {
	definition, ok := value.(map[string]any)
	if !ok {
		return nil, invalidInput(context+" must be a JSON object.", nil)
	}
	if err := requireExactKeys(
		definition,
		[]string{"argv", "name", "required"},
		[]string{"timeout_seconds"},
		context,
	); err != nil {
		return nil, err
	}
	name, err := requireNonemptyString(definition["name"], context+" name")
	if err != nil {
		return nil, err
	}
	argvValues, ok := definition["argv"].([]any)
	if !ok || len(argvValues) == 0 {
		return nil, invalidInput(context+" argv must be a non-empty array.", nil)
	}
	argv := make([]any, 0, len(argvValues))
	for index, raw := range argvValues {
		argument, err := requireNonemptyString(raw, fmt.Sprintf("%s argv[%d]", context, index))
		if err != nil {
			return nil, err
		}
		argv = append(argv, argument)
	}
	required, ok := definition["required"].(bool)
	if !ok {
		return nil, invalidInput(context+" required must be a boolean.", nil)
	}
	normalized := map[string]any{
		"name":     name,
		"argv":     argv,
		"required": required,
	}
	if timeout, exists := definition["timeout_seconds"]; exists {
		if !isExactPositiveInteger(timeout) {
			return nil, invalidInput(context+" timeout_seconds must be a positive integer.", nil)
		}
		normalized["timeout_seconds"] = timeout
	}
	return normalized, nil
}

func normalizeVerifier(value any) (map[string]any, error) {
	verifier, ok := value.(map[string]any)
	if !ok {
		return nil, invalidInput("Task Spec verifier must be a JSON object.", nil)
	}
	if err := requireExactKeys(verifier, []string{"required"}, []string{"preferred_runner"}, "Task Spec verifier"); err != nil {
		return nil, err
	}
	required, ok := verifier["required"].(bool)
	if !ok {
		return nil, invalidInput("Task Spec verifier required must be a boolean.", nil)
	}
	normalized := map[string]any{"required": required}
	if value, exists := verifier["preferred_runner"]; exists {
		preferred, err := requireNonemptyString(value, "Task Spec verifier preferred_runner")
		if err != nil {
			return nil, err
		}
		normalized["preferred_runner"] = preferred
	}
	return normalized, nil
}

func requireExactKeys(value map[string]any, required, optional []string, context string) error {
	requiredSet := make(map[string]struct{}, len(required))
	for _, key := range required {
		requiredSet[key] = struct{}{}
	}
	missing := make([]string, 0)
	for _, key := range required {
		if _, ok := value[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return invalidInput(
			fmt.Sprintf("%s is missing required field(s): %s.", context, strings.Join(missing, ", ")),
			nil,
		)
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for key := range requiredSet {
		allowed[key] = struct{}{}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	unexpected := unexpectedKeys(value, sortedMapSetKeys(allowed))
	if len(unexpected) != 0 {
		return invalidInput(
			fmt.Sprintf("%s has unexpected field(s): %s.", context, strings.Join(unexpected, ", ")),
			nil,
		)
	}
	return nil
}

func unexpectedKeys(value map[string]any, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	unexpected := make([]string, 0)
	for key := range value {
		if _, ok := allowedSet[key]; !ok {
			unexpected = append(unexpected, key)
		}
	}
	sort.Strings(unexpected)
	return unexpected
}

func sortedMapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapSetKeys(value map[string]struct{}) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func requireNonemptyString(value any, context string) (string, error) {
	text, ok := value.(string)
	if !ok || text == "" {
		return "", invalidInput(context+" must be a non-empty string.", nil)
	}
	return text, nil
}

func isExactIntegerValue(value any, want int64) bool {
	number, ok := value.(json.Number)
	if !ok || strings.ContainsAny(string(number), ".eE") {
		return false
	}
	integer := new(big.Int)
	if _, ok := integer.SetString(string(number), 10); !ok {
		return false
	}
	return integer.Cmp(big.NewInt(want)) == 0
}

func isExactPositiveInteger(value any) bool {
	number, ok := value.(json.Number)
	if !ok || strings.ContainsAny(string(number), ".eE") {
		return false
	}
	integer := new(big.Int)
	if _, ok := integer.SetString(string(number), 10); !ok {
		return false
	}
	return integer.Sign() > 0
}

func cloneCheckDefinition(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		if argv, ok := value.([]any); ok {
			clone[key] = append([]any(nil), argv...)
		} else {
			clone[key] = value
		}
	}
	return clone
}

func currentHead(repository string) (string, error) {
	command := exec.Command("git", "-C", repository, "rev-parse", "HEAD")
	stdout, err := command.Output()
	if err != nil {
		if _, missing := err.(*exec.Error); missing {
			return "", repositoryError("Git is required to record a task baseline.", err)
		}
		return "", repositoryError("Task creation requires a repository with a current HEAD.", err)
	}
	head := strings.TrimSpace(string(stdout))
	if head == "" {
		return "", repositoryError("Task creation requires a repository with a current HEAD.", nil)
	}
	return head, nil
}

func containsUnpairedJSONSurrogate(contents []byte) bool {
	inString := false
	for index := 0; index < len(contents); {
		character := contents[index]
		if !inString {
			if character == '"' {
				inString = true
			}
			index++
			continue
		}
		if character == '"' {
			inString = false
			index++
			continue
		}
		if character != '\\' {
			index++
			continue
		}
		if index+1 >= len(contents) || contents[index+1] != 'u' {
			index += 2
			continue
		}
		code, ok := decodeHexQuad(contents[index+2:])
		if !ok {
			index += 2
			continue
		}
		if code >= 0xd800 && code <= 0xdbff {
			if index+12 > len(contents) || contents[index+6] != '\\' || contents[index+7] != 'u' {
				return true
			}
			low, lowOK := decodeHexQuad(contents[index+8:])
			if !lowOK || low < 0xdc00 || low > 0xdfff {
				return true
			}
			index += 12
			continue
		}
		if code >= 0xdc00 && code <= 0xdfff {
			return true
		}
		index += 6
	}
	return false
}

func decodeHexQuad(value []byte) (uint16, bool) {
	if len(value) < 4 {
		return 0, false
	}
	var result uint16
	for _, character := range value[:4] {
		digit, ok := hexadecimalDigit(character)
		if !ok {
			return 0, false
		}
		result = result<<4 | uint16(digit)
	}
	return result, true
}
