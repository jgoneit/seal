package taskstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShowReadsStoredJSONObjectWithoutTaskSchemaValidation(t *testing.T) {
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)

	tests := []struct {
		name     string
		taskID   string
		contents string
		want     map[string]any
	}{
		{
			name:   "normalized task",
			taskID: "TASK-001",
			contents: `{
  "schema_version": 1,
  "id": "TASK-001",
  "type": "bugfix",
  "objective": "Keep the read boundary exact.",
  "scope": ["internal/taskstate"],
  "checks": [
    {
      "name": "unit-test",
      "argv": ["go", "test", "./..."],
      "required": true
    }
  ],
  "risk": "low",
  "verifier": {"required": false},
  "baseline": "0123456789abcdef"
}`,
			want: map[string]any{
				"schema_version": json.Number("1"),
				"id":             "TASK-001",
				"type":           "bugfix",
				"objective":      "Keep the read boundary exact.",
				"scope":          []any{"internal/taskstate"},
				"checks": []any{
					map[string]any{
						"name":     "unit-test",
						"argv":     []any{"go", "test", "./..."},
						"required": true,
					},
				},
				"risk":     "low",
				"verifier": map[string]any{"required": false},
				"baseline": "0123456789abcdef",
			},
		},
		{
			name:     "schema invalid object remains readable",
			taskID:   "TASK-INVALID-SCHEMA",
			contents: `{"schema_version":99,"id":"DIFFERENT","unexpected":{"value":9007199254740993}}`,
			want: map[string]any{
				"schema_version": json.Number("99"),
				"id":             "DIFFERENT",
				"unexpected": map[string]any{
					"value": json.Number("9007199254740993"),
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(tasks, test.taskID+".json")
			mustWriteFile(t, path, test.contents)
			before := mustReadFile(t, path)

			got, err := Show(repository, test.taskID)
			if err != nil {
				t.Fatalf("Show() error = %v", err)
			}
			if !reflect.DeepEqual(got.values, test.want) {
				t.Fatalf("Show() values = %#v, want %#v", got.values, test.want)
			}
			if after := mustReadFile(t, path); after != before {
				t.Fatalf("Show() changed stored Task: before %q, after %q", before, after)
			}
		})
	}
}

func TestShowRejectsInvalidStoredStateAsInvalidInput(t *testing.T) {
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)
	mustWriteFile(t, filepath.Join(tasks, "MALFORMED.json"), `{"broken":`)
	mustWriteFile(t, filepath.Join(tasks, "TRAILING.json"), `{} {}`)
	mustWriteFile(t, filepath.Join(tasks, "ARRAY.json"), `[]`)

	tests := []struct {
		name        string
		taskID      string
		wantMessage string
	}{
		{
			name:        "malformed JSON",
			taskID:      "MALFORMED",
			wantMessage: "Task snapshot 'MALFORMED' is not valid JSON:",
		},
		{
			name:        "trailing JSON value",
			taskID:      "TRAILING",
			wantMessage: "Task snapshot 'TRAILING' is not valid JSON:",
		},
		{
			name:        "non-object JSON",
			taskID:      "ARRAY",
			wantMessage: "Task snapshot 'ARRAY' must be a JSON object.",
		},
		{
			name:        "missing Task",
			taskID:      "MISSING",
			wantMessage: "Task 'MISSING' does not exist.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Show(repository, test.taskID)
			assertKind(t, err, InvalidInput)
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("Show() error = %q, want it to contain %q", err, test.wantMessage)
			}
		})
	}
}

func TestShowValidatesExactTaskIDBeforeRepositoryLookup(t *testing.T) {
	notRepository := t.TempDir()
	tests := []struct {
		name        string
		taskID      string
		wantMessage string
	}{
		{
			name:        "empty",
			taskID:      "",
			wantMessage: "Task id must be a non-empty string.",
		},
		{
			name:        "leading hyphen",
			taskID:      "-TASK",
			wantMessage: "Task id must begin with an alphanumeric character",
		},
		{
			name:        "leading underscore",
			taskID:      "_TASK",
			wantMessage: "Task id must begin with an alphanumeric character",
		},
		{
			name:        "traversal",
			taskID:      "../TASK",
			wantMessage: "Task id must begin with an alphanumeric character",
		},
		{
			name:        "embedded slash",
			taskID:      "TASK/../../escape",
			wantMessage: "Task id must contain only letters, numbers, underscores, or hyphens.",
		},
		{
			name:        "non-ASCII",
			taskID:      "태스크",
			wantMessage: "Task id must begin with an alphanumeric character",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Show(notRepository, test.taskID)
			assertKind(t, err, InvalidInput)
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("Show() error = %q, want it to contain %q", err, test.wantMessage)
			}
		})
	}
}

func TestShowClassifiesRepositoryDiscoveryFailure(t *testing.T) {
	_, err := Show(t.TempDir(), "TASK-001")
	assertKind(t, err, Repository)
	if got, want := err.Error(), "Task commands must run inside a Git repository."; got != want {
		t.Fatalf("Show() error = %q, want %q", got, want)
	}
}

func TestShowMatchesReferenceTaskSymlinkBehavior(t *testing.T) {
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)

	confinedTarget := filepath.Join(repository, ".seal", "confined-task.json")
	mustWriteFile(t, confinedTarget, `{"case":"confined file symlink"}`)
	mustSymlink(t, filepath.Join("..", "confined-task.json"), filepath.Join(tasks, "CONFINED.json"))

	external := t.TempDir()
	externalTarget := filepath.Join(external, "external-task.json")
	mustWriteFile(t, externalTarget, `{"case":"external file symlink"}`)
	// Frozen Reference task show has no repository-confinement check for the
	// stored Task path. This compatibility slice intentionally preserves that
	// security limitation instead of inventing a stricter read boundary.
	mustSymlink(t, externalTarget, filepath.Join(tasks, "EXTERNAL.json"))

	mustSymlink(t, "missing-target.json", filepath.Join(tasks, "BROKEN.json"))
	mustSymlink(t, "LOOP-B.json", filepath.Join(tasks, "LOOP-A.json"))
	mustSymlink(t, "LOOP-A.json", filepath.Join(tasks, "LOOP-B.json"))
	mustMkdirAll(t, filepath.Join(tasks, "directory-target"))
	mustSymlink(t, "directory-target", filepath.Join(tasks, "DIRECTORY.json"))

	tests := []struct {
		name     string
		taskID   string
		wantCase string
		wantErr  string
	}{
		{
			name:     "confined file symlink accepted",
			taskID:   "CONFINED",
			wantCase: "confined file symlink",
		},
		{
			name:     "external file symlink accepted",
			taskID:   "EXTERNAL",
			wantCase: "external file symlink",
		},
		{
			name:    "broken file symlink is missing",
			taskID:  "BROKEN",
			wantErr: "Task 'BROKEN' does not exist.",
		},
		{
			name:    "file symlink loop is missing",
			taskID:  "LOOP-A",
			wantErr: "Task 'LOOP-A' does not exist.",
		},
		{
			name:    "non-file target is missing",
			taskID:  "DIRECTORY",
			wantErr: "Task 'DIRECTORY' does not exist.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := Show(repository, test.taskID)
			if test.wantErr != "" {
				assertKind(t, err, InvalidInput)
				if err.Error() != test.wantErr {
					t.Fatalf("Show() error = %q, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Show() error = %v", err)
			}
			if got := document.values["case"]; got != test.wantCase {
				t.Fatalf("Show()[case] = %#v, want %q", got, test.wantCase)
			}
		})
	}
}

func TestShowFollowsTaskDirectorySymlinksLikeReference(t *testing.T) {
	repository := initRepository(t)
	sealRoot := filepath.Join(repository, ".seal")
	mustMkdirAll(t, sealRoot)

	confinedTasks := filepath.Join(repository, "stored-tasks")
	mustMkdirAll(t, confinedTasks)
	mustWriteFile(t, filepath.Join(confinedTasks, "CONFINED-DIR.json"), `{"case":"confined directory symlink"}`)
	mustSymlink(t, filepath.Join("..", "stored-tasks"), filepath.Join(sealRoot, "tasks"))

	document, err := Show(repository, "CONFINED-DIR")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if got := document.values["case"]; got != "confined directory symlink" {
		t.Fatalf("Show()[case] = %#v", got)
	}
}

func TestShowDoesNotWriteRepositoryState(t *testing.T) {
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "READ-ONLY.json")
	mustMkdirAll(t, filepath.Dir(path))
	mustWriteFile(t, path, `{"id":"READ-ONLY"}`)
	before := snapshotTree(t, repository)

	if _, err := Show(repository, "READ-ONLY"); err != nil {
		t.Fatalf("Show() error = %v", err)
	}

	after := snapshotTree(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Show() changed repository tree:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestRenderMatchesFrozenReferenceNumberNormalization(t *testing.T) {
	// The expected bytes below were captured from seal-legacy 0.3.0.dev0 at
	// commit 94bb931a7934efe31549d4c21dc7153e43f27a08 using this stored object.
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "NUMBERS.json")
	mustMkdirAll(t, filepath.Dir(path))
	mustWriteFile(t, path, `{
  "z": [
    900719925474099312345678901234567890,
    -900719925474099312345678901234567890,
    -0,
    -0.0,
    1.2300e+02,
    1e400,
    -1e400,
    1e-4000,
    -1e-4000,
    1e9,
    1e15,
    1e16,
    1e-4,
    1e-5,
    1.2345678901234567,
    NaN,
    Infinity,
    -Infinity,
    {"nested_z": 2.2250738585072014e-308, "nested_a": 5e-324}
  ],
  "a": "한글 <>&"
}`)

	document, err := Show(repository, "NUMBERS")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	rendered, err := Render(document)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	want := `{
  "a": "한글 <>&",
  "z": [
    900719925474099312345678901234567890,
    -900719925474099312345678901234567890,
    0,
    -0.0,
    123.0,
    Infinity,
    -Infinity,
    0.0,
    -0.0,
    1000000000.0,
    1000000000000000.0,
    1e+16,
    0.0001,
    1e-05,
    1.2345678901234567,
    NaN,
    Infinity,
    -Infinity,
    {
      "nested_a": 5e-324,
      "nested_z": 2.2250738585072014e-308
    }
  ]
}`
	if got := string(rendered); got != want {
		t.Fatalf("Render() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestShowMatchesCPythonIntegerConversionLimit(t *testing.T) {
	// Frozen CPython 3.14 accepts exactly 4300 decimal digits, but json.load
	// raises an uncaught ValueError at 4301 digits. A leading sign is excluded.
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)
	digits4300 := strings.Repeat("9", 4300)
	digits4301 := strings.Repeat("9", 4301)

	tests := []struct {
		name           string
		taskID         string
		contents       string
		wantRendered   string
		wantNumericErr bool
	}{
		{
			name:         "positive 4300 digits succeeds",
			taskID:       "POSITIVE-4300",
			contents:     `{"value":` + digits4300 + `}`,
			wantRendered: "{\n  \"value\": " + digits4300 + "\n}",
		},
		{
			name:         "negative 4300 digits succeeds",
			taskID:       "NEGATIVE-4300",
			contents:     `{"value":-` + digits4300 + `}`,
			wantRendered: "{\n  \"value\": -" + digits4300 + "\n}",
		},
		{
			name:           "positive 4301 nested digits fails",
			taskID:         "POSITIVE-4301",
			contents:       `{"nested":{"value":` + digits4301 + `}}`,
			wantNumericErr: true,
		},
		{
			name:           "negative 4301 digits fails",
			taskID:         "NEGATIVE-4301",
			contents:       `{"value":-` + digits4301 + `}`,
			wantNumericErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustWriteFile(t, filepath.Join(tasks, test.taskID+".json"), test.contents)
			document, err := Show(repository, test.taskID)
			if test.wantNumericErr {
				assertKind(t, err, NumericFailure)
				if !strings.Contains(err.Error(), "4300 digits") {
					t.Fatalf("Show() error = %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Show() error = %v", err)
			}
			rendered, err := Render(document)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if got := string(rendered); got != test.wantRendered {
				t.Fatalf("Render() output mismatch: got length %d, want length %d", len(got), len(test.wantRendered))
			}
		})
	}
}

func TestCPythonIntegerLimitDoesNotApplyToFloatExponentOrString(t *testing.T) {
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "NON-INTEGER-DIGITS.json")
	mustMkdirAll(t, filepath.Dir(path))
	digits4301 := strings.Repeat("9", 4301)
	mustWriteFile(t, path, `{
  "decimal": `+digits4301+`.0,
  "exponent": `+digits4301+`e0,
  "string": "`+digits4301+`"
}`)

	document, err := Show(repository, "NON-INTEGER-DIGITS")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	rendered, err := Render(document)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "{\n" +
		"  \"decimal\": Infinity,\n" +
		"  \"exponent\": Infinity,\n" +
		"  \"string\": \"" + digits4301 + "\"\n" +
		"}"
	if got := string(rendered); got != want {
		t.Fatalf("Render() output mismatch: got length %d, want length %d", len(got), len(want))
	}
}

func TestShowKeepsPythonConstantsOutOfStrings(t *testing.T) {
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "CONSTANT-STRINGS.json")
	mustMkdirAll(t, filepath.Dir(path))
	mustWriteFile(t, path, `{
  "constants": [NaN, Infinity, -Infinity],
  "strings": ["NaN", "Infinity", "-Infinity", "2e1000000"]
}`)

	document, err := Show(repository, "CONSTANT-STRINGS")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	rendered, err := Render(document)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := `{
  "constants": [
    NaN,
    Infinity,
    -Infinity
  ],
  "strings": [
    "NaN",
    "Infinity",
    "-Infinity",
    "2e1000000"
  ]
}`
	if got := string(rendered); got != want {
		t.Fatalf("Render() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestShowRejectsInvalidUTF8WithoutReplacement(t *testing.T) {
	// Frozen seal-legacy lets UnicodeDecodeError escape and exits 1 for this
	// raw byte sequence. EncodingFailure preserves that distinct exit category.
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "INVALID-UTF8.json")
	mustMkdirAll(t, filepath.Dir(path))
	contents := []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	_, err := Show(repository, "INVALID-UTF8")
	assertKind(t, err, EncodingFailure)
	if got, want := err.Error(), "Task snapshot 'INVALID-UTF8' is not valid UTF-8."; got != want {
		t.Fatalf("Show() error = %q, want %q", got, want)
	}
}

func TestRenderMatchesReferenceSurrogateEscapeBytes(t *testing.T) {
	// Expected bytes were captured from seal-legacy 0.3.0.dev0 at frozen commit
	// 94bb931a7934efe31549d4c21dc7153e43f27a08 through a hex-dumped stdout pipe.
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)

	tests := []struct {
		name     string
		taskID   string
		contents string
		want     []byte
	}{
		{
			name:     "DC80 emits raw byte 80",
			taskID:   "DC80",
			contents: `{"value":"\udc80"}`,
			want:     joinBytes([]byte("{\n  \"value\": \""), []byte{0x80}, []byte("\"\n}")),
		},
		{
			name:     "DCFF emits raw byte ff",
			taskID:   "DCFF",
			contents: `{"value":"\udcff"}`,
			want:     joinBytes([]byte("{\n  \"value\": \""), []byte{0xff}, []byte("\"\n}")),
		},
		{
			name:     "multiple surrogateescape bytes retain position",
			taskID:   "MULTIPLE",
			contents: `{"value":"\udc80x\udcff"}`,
			want: joinBytes(
				[]byte("{\n  \"value\": \""),
				[]byte{0x80, 'x', 0xff},
				[]byte("\"\n}"),
			),
		},
		{
			name:     "surrogateescape object key uses Python sort order",
			taskID:   "KEY",
			contents: `{"z":0,"\udcff":1,"a":2}`,
			want: joinBytes(
				[]byte("{\n  \"a\": 2,\n  \"z\": 0,\n  \""),
				[]byte{0xff},
				[]byte("\": 1\n}"),
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustWriteFile(t, filepath.Join(tasks, test.taskID+".json"), test.contents)
			document, err := Show(repository, test.taskID)
			if err != nil {
				t.Fatalf("Show() error = %v", err)
			}
			rendered, err := Render(document)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if !reflect.DeepEqual(rendered, test.want) {
				t.Fatalf("Render() bytes = % x, want % x", rendered, test.want)
			}
		})
	}
}

func TestRenderRejectsUnencodableUnpairedSurrogates(t *testing.T) {
	// The frozen CLI emits no stdout and exits 1 for these code units because
	// its stdout surrogateescape handler cannot encode them.
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)

	tests := []struct {
		name     string
		taskID   string
		contents string
	}{
		{name: "unpaired high D800", taskID: "D800", contents: `{"value":"\ud800"}`},
		{name: "low DC00", taskID: "DC00", contents: `{"value":"\udc00"}`},
		{name: "low DC7F", taskID: "DC7F", contents: `{"value":"\udc7f"}`},
		{name: "low DD00", taskID: "DD00", contents: `{"value":"\udd00"}`},
		{name: "low DFFF", taskID: "DFFF", contents: `{"value":"\udfff"}`},
		{name: "unpaired surrogate in key", taskID: "KEY-FAIL", contents: `{"\ud800":1}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustWriteFile(t, filepath.Join(tasks, test.taskID+".json"), test.contents)
			document, err := Show(repository, test.taskID)
			if err != nil {
				t.Fatalf("Show() error = %v", err)
			}
			rendered, err := Render(document)
			if rendered != nil {
				t.Fatalf("Render() bytes = % x, want nil", rendered)
			}
			assertKind(t, err, EncodingFailure)
			if !strings.Contains(err.Error(), "unpaired UTF-16 surrogate escape") {
				t.Fatalf("Render() error = %q", err)
			}
		})
	}
}

func TestRenderAcceptsPairedAndLiteralSurrogateText(t *testing.T) {
	repository := initRepository(t)
	tasks := filepath.Join(repository, ".seal", "tasks")
	mustMkdirAll(t, tasks)
	mustWriteFile(t, filepath.Join(tasks, "SURROGATE-TEXT.json"), `{
  "emoji": "\ud83d\ude00",
  "lowest_pair_with_dc80": "\ud800\udc80",
  "literal": "\\udcff",
  "replacement": "\ufffd"
}`)

	document, err := Show(repository, "SURROGATE-TEXT")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	rendered, err := Render(document)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := `{
  "emoji": "😀",
  "literal": "\\udcff",
  "lowest_pair_with_dc80": "𐂀",
  "replacement": "�"
}`
	if got := string(rendered); got != want {
		t.Fatalf("Render() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestSurrogateEscapeMarkerCannotCollideWithStoredStrings(t *testing.T) {
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "MARKER-COLLISION.json")
	mustMkdirAll(t, filepath.Dir(path))
	// The first key collides with U+FFFD during Go's provisional decode, while
	// its value contains the first internal marker candidate. The final render
	// must preserve both distinct keys and the literal private-use characters.
	mustWriteFile(t, path, `{
  "\udc80": "80",
  "\ufffd": "replacement key",
  "literal": "80"
}`)

	document, err := Show(repository, "MARKER-COLLISION")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	rendered, err := Render(document)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := joinBytes(
		[]byte("{\n  \"literal\": \"80\",\n  \""),
		[]byte{0x80},
		[]byte("\": \"80\",\n  \"�\": \"replacement key\"\n}"),
	)
	if !reflect.DeepEqual(rendered, want) {
		t.Fatalf("Render() bytes = % x, want % x", rendered, want)
	}
}

func TestShowRejectsMalformedPythonConstantToken(t *testing.T) {
	repository := initRepository(t)
	path := filepath.Join(repository, ".seal", "tasks", "BAD-CONSTANT.json")
	mustMkdirAll(t, filepath.Dir(path))
	mustWriteFile(t, path, `{"value":NaN1}`)

	_, err := Show(repository, "BAD-CONSTANT")
	assertKind(t, err, InvalidInput)
	if !strings.Contains(err.Error(), "is not valid JSON:") {
		t.Fatalf("Show() error = %q, want invalid JSON category", err)
	}
}

func TestKindOfRejectsUnrelatedErrors(t *testing.T) {
	if kind, ok := KindOf(errors.New("unrelated")); ok || kind != 0 {
		t.Fatalf("KindOf() = (%v, %v), want (0, false)", kind, ok)
	}
}

func joinBytes(parts ...[]byte) []byte {
	var joined []byte
	for _, part := range parts {
		joined = append(joined, part...)
	}
	return joined
}

func initRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	mustMkdirAll(t, repository)
	command := exec.Command("git", "-C", repository, "init", "--quiet")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return repository
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(contents)
}

func mustSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func assertKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want kind %v", want)
	}
	got, ok := KindOf(err)
	if !ok || got != want {
		t.Fatalf("KindOf(%v) = (%v, %v), want (%v, true)", err, got, ok, want)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[key] = "symlink:" + target
			return nil
		}
		if entry.IsDir() {
			snapshot[key] = "dir"
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[key] = fmt.Sprintf("file:%o:%x", info.Mode().Perm(), contents)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotTree(%q): %v", root, err)
	}
	return snapshot
}
