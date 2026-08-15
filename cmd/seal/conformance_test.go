package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	corpusReferenceCommit     = "94bb931a7934efe31549d4c21dc7153e43f27a08"
	corpusReferenceRepository = "https://github.com/jgoneit/seal-legacy"
	corpusReferenceVersion    = "0.3.0.dev0"
	corpusCaseCount           = 71
)

type corpusEnvelope struct {
	BaseFixture   corpusBaseFixture `json:"base_fixture"`
	Cases         []corpusCase      `json:"cases"`
	Identities    corpusIdentities  `json:"identities"`
	Reference     corpusReference   `json:"reference"`
	SchemaVersion int               `json:"schema_version"`
}

type corpusReference struct {
	CommandPrefix []string `json:"command_prefix"`
	Commit        string   `json:"commit"`
	Invocation    string   `json:"invocation"`
	Repository    string   `json:"repository"`
	Version       string   `json:"version"`
}

type corpusBaseFixture struct {
	OriginKind          string                    `json:"origin_kind"`
	OriginPath          string                    `json:"origin_path"`
	Path                string                    `json:"path"`
	ReferenceValidation corpusReferenceValidation `json:"reference_validation"`
	SHA256              map[string]string         `json:"sha256"`
}

type corpusReferenceValidation struct {
	GitStatusStable bool   `json:"git_status_before_after_equal_and_clean"`
	RunCommand      string `json:"run_command"`
	RunEvidenceSHA  string `json:"run_evidence_sha256"`
	RunExitCode     int    `json:"run_exit_code"`
	SealFilesStable bool   `json:"seal_file_set_size_sha256_before_after_equal"`
	StderrEmpty     bool   `json:"stderr_empty"`
	TaskCommand     string `json:"task_command"`
	TaskExitCode    int    `json:"task_exit_code"`
}

type corpusIdentities struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
}

type corpusCase struct {
	Base              string            `json:"base"`
	Command           []string          `json:"command"`
	ExpectedResult    string            `json:"expected_result"`
	ExpectedArtifacts map[string]string `json:"expected_artifact_values"`
	ID                string            `json:"id"`
	Mutations         []corpusMutation  `json:"mutations"`
	Observed          bool              `json:"observed_with_frozen_reference"`
}

type corpusMutation struct {
	File    string          `json:"file"`
	From    string          `json:"from"`
	Hex     string          `json:"hex"`
	Literal string          `json:"literal"`
	Op      string          `json:"op"`
	Path    string          `json:"path"`
	Pointer string          `json:"pointer"`
	Repeat  *corpusRepeat   `json:"repeat"`
	Target  string          `json:"target"`
	To      string          `json:"to"`
	Value   json.RawMessage `json:"value"`
}

type corpusRepeat struct {
	Count int    `json:"count"`
	Text  string `json:"text"`
}

type corpusResultsEnvelope struct {
	Normalization map[string]string       `json:"normalization"`
	Reference     corpusReference         `json:"reference"`
	Results       map[string]corpusResult `json:"results"`
	SchemaVersion int                     `json:"schema_version"`
}

type corpusResult struct {
	ExitCode       int             `json:"exit_code"`
	StderrCategory string          `json:"stderr_category"`
	StderrMessage  string          `json:"stderr_message"`
	StderrTerminal string          `json:"stderr_terminal_exception"`
	StdoutEmpty    bool            `json:"stdout_empty"`
	StdoutJSON     json.RawMessage `json:"stdout_json"`
	StdoutRawHex   *string         `json:"stdout_raw_hex"`
}

type corpusTreeEntry struct {
	Kind       string
	Mode       string
	Size       int64
	SHA256     string
	LinkTarget string
}

func TestRunCLIConformanceCorpusMatchesFrozenReferenceWithoutWrites(t *testing.T) {
	var corpus corpusEnvelope
	loadCorpusJSON(t, filepath.Join("..", "..", "conformance", "fixtures", "cases.json"), &corpus)
	var expected corpusResultsEnvelope
	loadCorpusJSON(t, filepath.Join("..", "..", "conformance", "expected", "reference-results.json"), &expected)
	validateCorpusMetadata(t, corpus, expected)
	validateCorpusFixtureHashes(t, filepath.Join("..", "..", filepath.FromSlash(corpus.BaseFixture.Path)), corpus.BaseFixture.SHA256)
	fixtureCopy := conformanceRepository(t)
	validateCorpusFixtureHashes(t, fixtureCopy, corpus.BaseFixture.SHA256)

	seen := make(map[string]struct{}, len(corpus.Cases))
	artifactCases := 0
	artifactValues := 0
	for _, testCase := range corpus.Cases {
		if testCase.ID == "" {
			t.Fatal("corpus contains an empty case id")
		}
		if _, duplicate := seen[testCase.ID]; duplicate {
			t.Fatalf("duplicate corpus case id %q", testCase.ID)
		}
		seen[testCase.ID] = struct{}{}
		if testCase.ExpectedResult != testCase.ID {
			t.Fatalf("case %q expected_result = %q", testCase.ID, testCase.ExpectedResult)
		}
		if !testCase.Observed {
			t.Fatalf("case %q was not observed with the frozen Reference", testCase.ID)
		}
		if _, ok := expected.Results[testCase.ExpectedResult]; !ok {
			t.Fatalf("case %q has no Reference result", testCase.ID)
		}
		if len(testCase.ExpectedArtifacts) != 0 {
			artifactCases++
			artifactValues += len(testCase.ExpectedArtifacts)
		}
	}
	if artifactCases != 2 || artifactValues != 4 {
		t.Fatalf("expected_artifact_values coverage = %d cases/%d values, want 2/4", artifactCases, artifactValues)
	}
	for resultID := range expected.Results {
		if _, ok := seen[resultID]; !ok {
			t.Fatalf("Reference result %q has no corpus case", resultID)
		}
	}

	for _, testCase := range corpus.Cases {
		testCase := testCase
		t.Run(testCase.ID, func(t *testing.T) {
			if len(testCase.Command) < 2 || testCase.Command[0] != "seal-legacy" {
				t.Fatalf("unsupported Reference command: %q", testCase.Command)
			}

			var repository string
			switch testCase.Base {
			case "base-pass":
				repository = conformanceRepository(t)
			case "":
				if testCase.ID != "run_repository_missing" && testCase.ID != "task_repository_missing" {
					t.Fatalf("case has no base fixture: %q", testCase.ID)
				}
				repository = filepath.Join(t.TempDir(), "not-a-repository")
				if err := os.MkdirAll(repository, 0o755); err != nil {
					t.Fatalf("create non-repository cwd: %v", err)
				}
			default:
				t.Fatalf("unsupported base fixture %q", testCase.Base)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatalf("create outside directory: %v", err)
			}
			for index, mutation := range testCase.Mutations {
				if err := applyCorpusMutation(repository, outside, mutation); err != nil {
					t.Fatalf("mutation %d (%s): %v", index, mutation.Op, err)
				}
			}
			assertCorpusExpectedArtifacts(t, repository, testCase, corpus.Identities)

			beforeRepository := snapshotCorpusTree(t, repository)
			beforeOutside := snapshotCorpusTree(t, outside)
			defer func() {
				afterRepository := snapshotCorpusTree(t, repository)
				afterOutside := snapshotCorpusTree(t, outside)
				if !reflect.DeepEqual(afterRepository, beforeRepository) {
					t.Errorf("command changed the disposable repository\nbefore: %#v\nafter: %#v", beforeRepository, afterRepository)
				}
				if !reflect.DeepEqual(afterOutside, beforeOutside) {
					t.Errorf("command changed the outside tree\nbefore: %#v\nafter: %#v", beforeOutside, afterOutside)
				}
			}()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runCLI(repository, testCase.Command[1:], &stdout, &stderr)
			compareCorpusResult(t, testCase.ID, repository, outside, expected.Results[testCase.ExpectedResult], code, stdout.Bytes(), stderr.Bytes())
		})
	}
}

func loadCorpusJSON(t *testing.T, path string, destination any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode %q: %v", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("decode trailing content in %q: %v", path, err)
	}
}

func validateCorpusMetadata(t *testing.T, corpus corpusEnvelope, expected corpusResultsEnvelope) {
	t.Helper()
	if corpus.SchemaVersion != 1 || expected.SchemaVersion != 1 {
		t.Fatalf("schema versions = cases:%d results:%d, want 1/1", corpus.SchemaVersion, expected.SchemaVersion)
	}
	if len(corpus.Cases) != corpusCaseCount || len(expected.Results) != corpusCaseCount {
		t.Fatalf("corpus sizes = cases:%d results:%d, want %d/%d", len(corpus.Cases), len(expected.Results), corpusCaseCount, corpusCaseCount)
	}
	for label, reference := range map[string]corpusReference{"cases": corpus.Reference, "results": expected.Reference} {
		if reference.Commit != corpusReferenceCommit || reference.Repository != corpusReferenceRepository || reference.Version != corpusReferenceVersion {
			t.Fatalf("%s Reference metadata = commit:%q repository:%q version:%q", label, reference.Commit, reference.Repository, reference.Version)
		}
	}
	wantPrefix := []string{"PYTHONPATH=<seal-legacy>/src", "<seal-legacy>/.venv/bin/python", "-m", "seal_legacy"}
	if !reflect.DeepEqual(corpus.Reference.CommandPrefix, wantPrefix) {
		t.Fatalf("Reference command_prefix = %q, want %q", corpus.Reference.CommandPrefix, wantPrefix)
	}
	wantInvocation := "PYTHONPATH=<seal-legacy>/src <seal-legacy>/.venv/bin/python -m seal_legacy"
	if expected.Reference.Invocation != wantInvocation {
		t.Fatalf("Reference invocation = %q, want %q", expected.Reference.Invocation, wantInvocation)
	}
	if corpus.Identities.TaskID != fixtureTaskID || corpus.Identities.RunID != fixtureRunID {
		t.Fatalf("corpus identities = %q/%q, want %q/%q", corpus.Identities.TaskID, corpus.Identities.RunID, fixtureTaskID, fixtureRunID)
	}
	base := corpus.BaseFixture
	if base.OriginKind != "Reference CLI-generated valid Evidence Run at the frozen commit" ||
		base.OriginPath != ".seal/evidence/"+fixtureTaskID+"/"+fixtureRunID ||
		base.Path != "conformance/fixtures/base-pass" || len(base.SHA256) != 11 {
		t.Fatalf("unexpected base fixture provenance: kind:%q origin:%q path:%q hashes:%d", base.OriginKind, base.OriginPath, base.Path, len(base.SHA256))
	}
	validation := base.ReferenceValidation
	wantRunCommand := "seal-legacy run show " + fixtureTaskID + " --run-id " + fixtureRunID
	wantTaskCommand := "seal-legacy task show " + fixtureTaskID
	if !validation.GitStatusStable || !validation.SealFilesStable || !validation.StderrEmpty ||
		validation.RunExitCode != 0 || validation.TaskExitCode != 0 ||
		validation.RunCommand != wantRunCommand || validation.TaskCommand != wantTaskCommand ||
		validation.RunEvidenceSHA != "68443492cc30f09c4517d2c74d1fbc7bd27945b703b674cc588830b933e2c516" {
		t.Fatalf("unexpected base fixture Reference validation: %#v", validation)
	}
	wantNormalization := map[string]string{
		"numeric_conversion_failure": "CPython integer conversion limits are compared by exit code, category, and terminal ValueError; environment-specific traceback frames are ignored.",
		"stderr":                     "Recorded verbatim after replacing any disposable repository path with <repo>; category and exit code are conformance authority.",
		"stderr_terminal_exception":  "For an unhandled failure, the terminal exception class is authoritative after environment-specific traceback frames are ignored.",
		"stdout":                     "Parsed as exactly one JSON object when non-empty; JSON key order and whitespace are not compared.",
		"stdout_raw_hex":             "When present, exact stdout bytes are authoritative; stdout_json is null when bytes are not strict UTF-8 JSON or contain Python-only numeric constants.",
	}
	if !reflect.DeepEqual(expected.Normalization, wantNormalization) {
		t.Fatalf("Reference normalization metadata changed\ngot:  %#v\nwant: %#v", expected.Normalization, wantNormalization)
	}
}

func validateCorpusFixtureHashes(t *testing.T, root string, expected map[string]string) {
	t.Helper()
	actual := make(map[string]string, len(expected))
	sealRoot := filepath.Join(root, ".seal")
	err := filepath.WalkDir(sealRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("base fixture contains non-regular file %q (%s)", path, info.Mode())
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		actual[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatalf("validate base fixture %q: %v", root, err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("base fixture file-set/hash mismatch at %q\ngot:  %#v\nwant: %#v", root, actual, expected)
	}
}

func assertCorpusExpectedArtifacts(t *testing.T, repository string, testCase corpusCase, identities corpusIdentities) {
	t.Helper()
	runDirectory := filepath.Join(repository, ".seal", "evidence", identities.TaskID, identities.RunID)
	for name, want := range testCase.ExpectedArtifacts {
		var path string
		var pointer string
		switch name {
		case "evidence_sha256", "generated_manifest_evidence_sha256":
			path = filepath.Join(runDirectory, "run-manifest.json")
			pointer = "/evidence_sha256"
		case "source_before_checks_sha256":
			path = filepath.Join(runDirectory, "source-before-checks.json")
			pointer = "/snapshot_sha256"
		case "source_after_checks_sha256":
			path = filepath.Join(runDirectory, "source-after-checks.json")
			pointer = "/snapshot_sha256"
		default:
			t.Fatalf("case %q has unsupported expected_artifact_values key %q", testCase.ID, name)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("case %q read expected artifact %q: %v", testCase.ID, path, err)
		}
		raw, err := getCorpusRaw(contents, pointer)
		if err != nil {
			t.Fatalf("case %q read %s%s: %v", testCase.ID, path, pointer, err)
		}
		var got string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("case %q decode %s%s: %v", testCase.ID, path, pointer, err)
		}
		if got != want {
			t.Fatalf("case %q artifact %q = %q, want %q", testCase.ID, name, got, want)
		}
	}
}

func snapshotCorpusTree(t *testing.T, root string) map[string]corpusTreeEntry {
	t.Helper()
	snapshot := make(map[string]corpusTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		record := corpusTreeEntry{Mode: fmt.Sprintf("%#o", uint32(info.Mode()))}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			record.Kind = "symlink"
			record.LinkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		case info.IsDir():
			record.Kind = "directory"
		case info.Mode().IsRegular():
			record.Kind = "regular"
			record.Size = info.Size()
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(contents)
			record.SHA256 = hex.EncodeToString(digest[:])
		default:
			record.Kind = "other"
			record.Size = info.Size()
		}
		snapshot[filepath.ToSlash(relative)] = record
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree %q: %v", root, err)
	}
	return snapshot
}

func applyCorpusMutation(repository, outside string, mutation corpusMutation) error {
	resolve := func(path string) string { return resolveCorpusPath(repository, outside, path) }
	switch mutation.Op {
	case "set":
		return mutateCorpusJSON(resolve(mutation.File), mutation.Pointer, mutation.Value, false)
	case "append":
		return mutateCorpusJSON(resolve(mutation.File), mutation.Pointer, mutation.Value, true)
	case "write_json":
		contents, err := prettyCorpusJSON(mutation.Value)
		if err != nil {
			return err
		}
		return writeCorpusFile(resolve(mutation.File), contents)
	case "write_utf8":
		var value string
		if err := json.Unmarshal(mutation.Value, &value); err != nil {
			return fmt.Errorf("decode write_utf8 value: %w", err)
		}
		return writeCorpusFile(resolve(mutation.File), []byte(value))
	case "write_bytes_hex":
		contents, err := hex.DecodeString(mutation.Hex)
		if err != nil {
			return fmt.Errorf("decode bytes: %w", err)
		}
		return writeCorpusFile(resolve(mutation.File), contents)
	case "delete":
		return os.Remove(resolve(mutation.File))
	case "mkdir":
		return os.MkdirAll(resolve(mutation.Path), 0o755)
	case "rename":
		to := resolve(mutation.To)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.Rename(resolve(mutation.From), to)
	case "symlink":
		path := resolve(mutation.File)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.Symlink(resolveCorpusTarget(outside, mutation.Target), path)
	case "copy_outside_repository":
		return copyCorpusFile(resolve(mutation.From), resolve(mutation.Target))
	case "copy_value":
		fromFile, fromPointer, err := splitCorpusLocator(mutation.From)
		if err != nil {
			return err
		}
		toFile, toPointer, err := splitCorpusLocator(mutation.To)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(resolve(fromFile))
		if err != nil {
			return err
		}
		value, err := getCorpusRaw(contents, fromPointer)
		if err != nil {
			return err
		}
		return mutateCorpusJSON(resolve(toFile), toPointer, value, false)
	case "recompute_snapshot_sha256":
		return recomputeCorpusSnapshot(resolve(mutation.File))
	case "rebuild_run_manifest":
		return rebuildCorpusManifest(repository, outside, mutation)
	case "set_raw_number":
		literal, err := corpusNumberLiteral(mutation)
		if err != nil {
			return err
		}
		return mutateCorpusJSON(resolve(mutation.File), mutation.Pointer, json.RawMessage(literal), false)
	default:
		return fmt.Errorf("unsupported corpus operation %q", mutation.Op)
	}
}

func resolveCorpusPath(repository, outside, path string) string {
	if strings.HasPrefix(path, "<outside>") {
		return filepath.Join(outside, filepath.FromSlash(strings.TrimPrefix(path, "<outside>/")))
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repository, filepath.FromSlash(path))
}

func resolveCorpusTarget(outside, target string) string {
	if strings.HasPrefix(target, "<outside>") {
		return filepath.Join(outside, filepath.FromSlash(strings.TrimPrefix(target, "<outside>/")))
	}
	return filepath.FromSlash(target)
}

func copyCorpusFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, contents, 0o644)
}

func writeCorpusFile(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, contents, mode)
}

func splitCorpusLocator(locator string) (string, string, error) {
	parts := strings.SplitN(locator, "#", 2)
	if len(parts) != 2 || parts[0] == "" || !strings.HasPrefix(parts[1], "/") {
		return "", "", fmt.Errorf("invalid JSON locator %q", locator)
	}
	return parts[0], parts[1], nil
}

func corpusNumberLiteral(mutation corpusMutation) (string, error) {
	if mutation.Repeat != nil {
		if mutation.Literal != "" || mutation.Repeat.Count < 1 || mutation.Repeat.Text == "" {
			return "", fmt.Errorf("invalid repeat number recipe")
		}
		return strings.Repeat(mutation.Repeat.Text, mutation.Repeat.Count), nil
	}
	if mutation.Literal == "" {
		return "", fmt.Errorf("missing raw number literal")
	}
	return mutation.Literal, nil
}

func mutateCorpusJSON(path, pointer string, value json.RawMessage, appendValue bool) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tokens, err := corpusPointerTokens(pointer)
	if err != nil {
		return err
	}
	updated, err := updateCorpusRaw(contents, tokens, value, appendValue)
	if err != nil {
		return err
	}
	pretty, err := prettyCorpusJSON(updated)
	if err != nil {
		return err
	}
	return writeCorpusFile(path, pretty)
}

func corpusPointerTokens(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		parts[index] = part
	}
	return parts, nil
}

func updateCorpusRaw(raw json.RawMessage, tokens []string, value json.RawMessage, appendValue bool) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(tokens) == 0 {
		if !appendValue {
			if !json.Valid(value) {
				return nil, fmt.Errorf("replacement is not valid JSON")
			}
			return append(json.RawMessage(nil), value...), nil
		}
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, fmt.Errorf("append target is not an array: %w", err)
		}
		array = append(array, append(json.RawMessage(nil), value...))
		return marshalCorpusJSON(array)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty JSON document")
	}
	switch raw[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		child, exists := object[tokens[0]]
		if !exists && len(tokens) > 1 {
			return nil, fmt.Errorf("JSON pointer member %q is missing", tokens[0])
		}
		updated, err := updateCorpusRaw(child, tokens[1:], value, appendValue)
		if err != nil {
			return nil, err
		}
		object[tokens[0]] = updated
		return marshalCorpusJSON(object)
	case '[':
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, err
		}
		index, err := strconv.Atoi(tokens[0])
		if err != nil || index < 0 || index >= len(array) {
			return nil, fmt.Errorf("invalid JSON array index %q", tokens[0])
		}
		updated, err := updateCorpusRaw(array[index], tokens[1:], value, appendValue)
		if err != nil {
			return nil, err
		}
		array[index] = updated
		return marshalCorpusJSON(array)
	default:
		return nil, fmt.Errorf("JSON pointer traverses a scalar")
	}
}

func getCorpusRaw(contents []byte, pointer string) (json.RawMessage, error) {
	tokens, err := corpusPointerTokens(pointer)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(bytes.TrimSpace(contents))
	for _, token := range tokens {
		if len(raw) == 0 {
			return nil, fmt.Errorf("empty JSON value")
		}
		switch raw[0] {
		case '{':
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				return nil, err
			}
			var ok bool
			raw, ok = object[token]
			if !ok {
				return nil, fmt.Errorf("JSON pointer member %q is missing", token)
			}
		case '[':
			var array []json.RawMessage
			if err := json.Unmarshal(raw, &array); err != nil {
				return nil, err
			}
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(array) {
				return nil, fmt.Errorf("invalid JSON array index %q", token)
			}
			raw = array[index]
		default:
			return nil, fmt.Errorf("JSON pointer traverses a scalar")
		}
	}
	return append(json.RawMessage(nil), raw...), nil
}

func prettyCorpusJSON(raw json.RawMessage) ([]byte, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON mutation result")
	}
	canonical, err := canonicalCorpusJSON(raw)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := json.Indent(&output, canonical, "", "  "); err != nil {
		return nil, err
	}
	output.WriteByte('\n')
	return output.Bytes(), nil
}

func canonicalCorpusJSON(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON")
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty JSON")
	}
	switch raw[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		for key, value := range object {
			canonical, err := canonicalCorpusJSON(value)
			if err != nil {
				return nil, err
			}
			object[key] = canonical
		}
		return marshalCorpusJSON(object)
	case '[':
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, err
		}
		for index, value := range array {
			canonical, err := canonicalCorpusJSON(value)
			if err != nil {
				return nil, err
			}
			array[index] = canonical
		}
		return marshalCorpusJSON(array)
	default:
		return append(json.RawMessage(nil), raw...), nil
	}
}

func recomputeCorpusSnapshot(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(contents, &document); err != nil {
		return err
	}
	delete(document, "snapshot_sha256")
	payload, err := marshalCorpusJSON(document)
	if err != nil {
		return err
	}
	payload, err = canonicalCorpusJSON(payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	digestJSON, _ := json.Marshal(hex.EncodeToString(digest[:]))
	document["snapshot_sha256"] = digestJSON
	updated, err := marshalCorpusJSON(document)
	if err != nil {
		return err
	}
	pretty, err := prettyCorpusJSON(updated)
	if err != nil {
		return err
	}
	return writeCorpusFile(path, pretty)
}

func rebuildCorpusManifest(repository, outside string, mutation corpusMutation) error {
	fromFile, fromPointer, err := splitCorpusLocator(mutation.From)
	if err != nil {
		return err
	}
	verification, err := os.ReadFile(resolveCorpusPath(repository, outside, fromFile))
	if err != nil {
		return err
	}
	rawPaths, err := getCorpusRaw(verification, fromPointer)
	if err != nil {
		return err
	}
	var paths []string
	if err := json.Unmarshal(rawPaths, &paths); err != nil {
		return err
	}
	sort.Strings(paths)

	manifestPath := resolveCorpusPath(repository, outside, mutation.File)
	runDirectory := filepath.Dir(manifestPath)
	existing, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var oldManifest struct {
		CreatedAt string `json:"created_at"`
		RunID     string `json:"run_id"`
		TaskID    string `json:"task_id"`
	}
	if err := json.Unmarshal(existing, &oldManifest); err != nil {
		return err
	}

	records := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(filepath.Join(runDirectory, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("read manifest file %q: %w", path, err)
		}
		digest := sha256.Sum256(contents)
		records = append(records, map[string]any{
			"path":       path,
			"sha256":     hex.EncodeToString(digest[:]),
			"size_bytes": len(contents),
		})
	}
	payload := map[string]any{
		"files":          records,
		"run_id":         oldManifest.RunID,
		"schema_version": 1,
		"task_id":        oldManifest.TaskID,
	}
	canonical, err := marshalCorpusJSON(payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	document := map[string]any{
		"created_at":      oldManifest.CreatedAt,
		"evidence_sha256": hex.EncodeToString(digest[:]),
		"files":           records,
		"run_id":          oldManifest.RunID,
		"schema_version":  1,
		"task_id":         oldManifest.TaskID,
	}
	updated, err := marshalCorpusJSON(document)
	if err != nil {
		return err
	}
	pretty, err := prettyCorpusJSON(updated)
	if err != nil {
		return err
	}
	return writeCorpusFile(manifestPath, pretty)
}

func marshalCorpusJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func compareCorpusResult(t *testing.T, caseID, repository, outside string, expected corpusResult, code int, stdout, stderr []byte) {
	t.Helper()
	if code != expected.ExitCode {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, expected.ExitCode, stderr)
	}
	if got := corpusStderrCategory(code, stderr); got != expected.StderrCategory {
		t.Fatalf("stderr category = %q, want %q; stderr = %q", got, expected.StderrCategory, stderr)
	}
	if expected.StderrMessage == "" {
		if len(stderr) != 0 {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
	} else if !bytes.HasSuffix(stderr, []byte{'\n'}) || bytes.Count(stderr, []byte{'\n'}) != 1 {
		t.Fatalf("stderr must be exactly one newline-terminated line: %q", stderr)
	}
	gotMessage, err := normalizeCorpusStderrMessage(caseID, strings.TrimSuffix(string(stderr), "\n"), "", repository, outside, false)
	if err != nil {
		t.Fatalf("normalize Candidate stderr: %v", err)
	}
	wantMessage, err := normalizeCorpusStderrMessage(caseID, expected.StderrMessage, expected.StderrTerminal, repository, outside, true)
	if err != nil {
		t.Fatalf("normalize recorded stderr: %v", err)
	}
	if gotMessage != wantMessage {
		t.Fatalf("stderr message shape mismatch\ngot:  %q\nwant: %q\nraw Candidate: %q\nraw recorded: %q", gotMessage, wantMessage, stderr, expected.StderrMessage)
	}
	if expected.StdoutEmpty && len(stdout) != 0 {
		t.Fatalf("stdout = % x, want empty", stdout)
	}
	if expected.StdoutRawHex != nil {
		want, err := hex.DecodeString(*expected.StdoutRawHex)
		if err != nil {
			t.Fatalf("decode expected stdout_raw_hex: %v", err)
		}
		if !bytes.Equal(stdout, want) {
			t.Fatalf("stdout raw bytes mismatch\ngot:  %x\nwant: %x", stdout, want)
		}
		return
	}
	if bytes.Equal(bytes.TrimSpace(expected.StdoutJSON), []byte("null")) {
		if !expected.StdoutEmpty {
			t.Fatal("Reference result has neither semantic nor raw stdout")
		}
		return
	}
	got := decodeCorpusStdout(t, stdout)
	wantBytes := append([]byte(nil), bytes.TrimSpace(expected.StdoutJSON)...)
	want := decodeCorpusStdout(t, append(wantBytes, '\n'))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stdout semantic JSON mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func normalizeCorpusStderrMessage(caseID, message, terminalException, repository, outside string, recorded bool) (string, error) {
	message = replaceCorpusDynamicPath(message, repository, "<repo>")
	message = replaceCorpusDynamicPath(message, outside, "<outside>")
	switch caseID {
	case "task_malformed_json":
		prefix := "error: Task snapshot '" + fixtureTaskID + "' is not valid JSON:"
		if !strings.HasPrefix(message, prefix) || strings.TrimSpace(strings.TrimPrefix(message, prefix)) == "" {
			return "", fmt.Errorf("malformed-JSON parser message does not have the recorded prefix: %q", message)
		}
		return prefix + " <parser-detail>", nil
	case "run_task_integer_digit_limit":
		if recorded {
			if terminalException != "ValueError" || message != "ValueError: Exceeds the limit (4300 digits) for integer string conversion: value has 4301 digits; use sys.set_int_max_str_digits() to increase the limit" {
				return "", fmt.Errorf("unexpected recorded numeric conversion failure: %q/%q", terminalException, message)
			}
		} else if terminalException != "" || message != "error: JSON integer token exceeds the frozen Python runtime limit of 4300 digits." {
			return "", fmt.Errorf("unexpected Candidate numeric conversion failure: %q", message)
		}
		return "numeric-conversion-failure:ValueError:limit=4300:digits=4301", nil
	case "task_integer_digit_limit":
		if recorded {
			if terminalException != "ValueError" || message != "ValueError: Exceeds the limit (4300 digits) for integer string conversion: value has 4301 digits; use sys.set_int_max_str_digits() to increase the limit" {
				return "", fmt.Errorf("unexpected recorded numeric conversion failure: %q/%q", terminalException, message)
			}
		} else if terminalException != "" || message != "error: Task snapshot '"+fixtureTaskID+"' contains a JSON integer exceeding CPython's limit of 4300 digits." {
			return "", fmt.Errorf("unexpected Candidate numeric conversion failure: %q", message)
		}
		return "numeric-conversion-failure:ValueError:limit=4300:digits=4301", nil
	case "task_invalid_raw_utf8":
		if recorded {
			if terminalException != "UnicodeDecodeError" || message != "UnicodeDecodeError: 'utf-8' codec can't decode byte 0xff in position 10: invalid start byte" {
				return "", fmt.Errorf("unexpected recorded encoding failure: %q/%q", terminalException, message)
			}
		} else if terminalException != "" || message != "error: Task snapshot '"+fixtureTaskID+"' is not valid UTF-8." {
			return "", fmt.Errorf("unexpected Candidate encoding failure: %q", message)
		}
		return "encoding-failure:UnicodeDecodeError:utf-8:byte=ff:position=10", nil
	default:
		if terminalException != "" {
			return "", fmt.Errorf("unexpected terminal exception %q for case %q", terminalException, caseID)
		}
		if message == "" {
			return "", nil
		}
		if strings.ContainsAny(message, "\r\n") || !strings.HasPrefix(message, "error: ") || strings.TrimSpace(strings.TrimPrefix(message, "error: ")) == "" {
			kind := "Candidate"
			if recorded {
				kind = "recorded"
			}
			return "", fmt.Errorf("%s handled stderr is not one nonempty error shape: %q", kind, message)
		}
		return "handled-error:<nonempty>", nil
	}
}

func replaceCorpusDynamicPath(message, path, placeholder string) string {
	if path == "" {
		return message
	}
	message = strings.ReplaceAll(message, path, placeholder)
	return strings.ReplaceAll(message, filepath.ToSlash(path), placeholder)
}

func decodeCorpusStdout(t *testing.T, contents []byte) any {
	t.Helper()
	if len(contents) == 0 || contents[len(contents)-1] != '\n' {
		t.Fatalf("stdout must end in one newline: %q", contents)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if _, ok := value.(map[string]any); !ok {
		t.Fatalf("stdout JSON is %T, want object", value)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("stdout contains trailing JSON: %v", err)
	}
	return value
}

func corpusStderrCategory(code int, stderr []byte) string {
	message := string(stderr)
	switch code {
	case 0:
		if len(stderr) == 0 {
			return "none"
		}
	case 1:
		if strings.Contains(message, "UTF-8") || strings.Contains(message, "UnicodeDecodeError") {
			return "unhandled-encoding-failure"
		}
		if strings.Contains(message, "4300 digits") || strings.Contains(message, "ValueError") {
			return "unhandled-numeric-conversion-failure"
		}
	case 2:
		if strings.HasPrefix(message, "error: ") {
			return "invalid-input-or-identity"
		}
	case 3:
		if strings.HasPrefix(message, "error: ") {
			return "repository-error"
		}
	case 8:
		if strings.HasPrefix(message, "error: ") {
			return "evidence-missing-or-corrupt"
		}
	}
	return "unexpected"
}
