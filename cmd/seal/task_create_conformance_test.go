package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

const createContractTaskID = "TASK-CREATE-001"

type createContractFixture struct {
	repository string
	input      string
}

type createContractResult struct {
	code   int
	stdout []byte
	stderr string
}

type createContractTree map[string]string

func TestTaskCreateContractNormalizesSupportedInputs(t *testing.T) {
	tests := []struct {
		name         string
		catalog      any
		catalogRaw   string
		spec         map[string]any
		wantChecks   []any
		wantVerifier map[string]any
	}{
		{
			name: "list catalog references preserve Task order and duplicates",
			catalog: map[string]any{
				"schema_version": 1,
				"checks": []any{
					map[string]any{"name": "first", "argv": []any{"go", "test", "./..."}, "required": true},
					map[string]any{"name": "second", "argv": []any{"go", "vet", "./..."}, "required": false, "timeout_seconds": 45},
				},
			},
			spec: func() map[string]any {
				spec := createContractValidSpec()
				spec["checks"] = []any{"second", "first", "second"}
				return spec
			}(),
			wantChecks: []any{
				map[string]any{"name": "second", "argv": []any{"go", "vet", "./..."}, "required": false, "timeout_seconds": 45},
				map[string]any{"name": "first", "argv": []any{"go", "test", "./..."}, "required": true},
				map[string]any{"name": "second", "argv": []any{"go", "vet", "./..."}, "required": false, "timeout_seconds": 45},
			},
			wantVerifier: map[string]any{"required": false},
		},
		{
			name: "object catalog injects an omitted name and permits omitted schema version",
			catalog: map[string]any{
				"checks": map[string]any{
					"lint": map[string]any{"argv": []any{"go", "fmt", "./..."}, "required": true},
				},
			},
			spec: func() map[string]any {
				spec := createContractValidSpec()
				spec["checks"] = []any{"lint"}
				return spec
			}(),
			wantChecks: []any{
				map[string]any{"name": "lint", "argv": []any{"go", "fmt", "./..."}, "required": true},
			},
			wantVerifier: map[string]any{"required": false},
		},
		{
			name: "inline check keeps omitted timeout and verifier preferred runner",
			catalog: map[string]any{
				"schema_version": 1,
				"checks":         []any{},
			},
			spec: func() map[string]any {
				spec := createContractValidSpec()
				spec["checks"] = []any{
					map[string]any{"name": "syntax", "argv": []any{"go", "test", "./cmd/seal"}, "required": false},
				}
				spec["verifier"] = map[string]any{"required": true, "preferred_runner": "review-agent"}
				return spec
			}(),
			wantChecks: []any{
				map[string]any{"name": "syntax", "argv": []any{"go", "test", "./cmd/seal"}, "required": false},
			},
			wantVerifier: map[string]any{"required": true, "preferred_runner": "review-agent"},
		},
		{
			name:       "duplicate object key uses the last JSON member",
			catalogRaw: `{"checks":{"same":{"argv":["first"],"required":false},"same":{"argv":["last"],"required":true}}}` + "\n",
			spec: func() map[string]any {
				spec := createContractValidSpec()
				spec["checks"] = []any{"same"}
				return spec
			}(),
			wantChecks: []any{
				map[string]any{"name": "same", "argv": []any{"last"}, "required": true},
			},
			wantVerifier: map[string]any{"required": false},
		},
		{
			name: "timeout accepts an integer larger than int64",
			catalog: map[string]any{
				"schema_version": 1,
				"checks":         []any{},
			},
			spec: func() map[string]any {
				spec := createContractValidSpec()
				spec["checks"] = []any{
					map[string]any{
						"name":            "large-timeout",
						"argv":            []any{"true"},
						"required":        true,
						"timeout_seconds": json.Number("9223372036854775808"),
					},
				}
				return spec
			}(),
			wantChecks: []any{
				map[string]any{
					"name":            "large-timeout",
					"argv":            []any{"true"},
					"required":        true,
					"timeout_seconds": json.Number("9223372036854775808"),
				},
			},
			wantVerifier: map[string]any{"required": false},
		},
		{
			name: "timeout accepts the frozen 4300 digit integer boundary",
			catalog: map[string]any{
				"schema_version": 1,
				"checks":         []any{},
			},
			spec: func() map[string]any {
				spec := createContractValidSpec()
				spec["checks"] = []any{
					map[string]any{
						"name":            "large-timeout",
						"argv":            []any{"true"},
						"required":        true,
						"timeout_seconds": json.Number(strings.Repeat("9", 4300)),
					},
				}
				return spec
			}(),
			wantChecks: []any{
				map[string]any{
					"name":            "large-timeout",
					"argv":            []any{"true"},
					"required":        true,
					"timeout_seconds": json.Number(strings.Repeat("9", 4300)),
				},
			},
			wantVerifier: map[string]any{"required": false},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createContractNewFixture(t, true)
			if test.catalogRaw != "" {
				createContractWriteRaw(t, createContractCatalogPath(fixture.repository), []byte(test.catalogRaw))
			} else {
				createContractWriteJSON(t, createContractCatalogPath(fixture.repository), test.catalog)
			}
			createContractWriteJSON(t, fixture.input, test.spec)
			inputBefore := createContractReadFile(t, fixture.input)
			catalogBefore := createContractReadFile(t, createContractCatalogPath(fixture.repository))

			result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
			baseline := createContractGit(t, fixture.repository, "rev-parse", "HEAD")
			want := createContractCopyMap(test.spec)
			want["baseline"] = baseline
			want["scope"] = []any{"src/seal", "tests/unit"}
			want["checks"] = test.wantChecks
			want["verifier"] = test.wantVerifier
			createContractAssertSuccess(t, fixture.repository, createContractTaskID, result, want)

			createContractRequireBytesEqual(t, fixture.input, inputBefore)
			createContractRequireBytesEqual(t, createContractCatalogPath(fixture.repository), catalogBefore)
		})
	}
}

func TestTaskCreateContractPreservesUnicodeNormalizationAndOrdering(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	catalog := map[string]any{
		"schema_version": 1,
		"checks": []any{
			map[string]any{"name": "first", "argv": []any{"printf", "첫째"}, "required": true},
			map[string]any{"name": "second", "argv": []any{"printf", "둘째"}, "required": false},
		},
	}
	createContractWriteJSON(t, createContractCatalogPath(fixture.repository), catalog)
	spec := createContractValidSpec()
	spec["objective"] = "  목표 <>& \u2028 \u2029 \x00  "
	spec["scope"] = []any{
		"./src//seal/",
		`tests\unit`,
		"./",
		"a//./b",
		"src/..hidden",
		`tests\unit`,
	}
	spec["checks"] = []any{"second", "first", "second"}
	spec["verifier"] = map[string]any{"required": true, "preferred_runner": " "}
	createContractWriteJSON(t, fixture.input, spec)

	result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	want := createContractCopyMap(spec)
	want["baseline"] = createContractGit(t, fixture.repository, "rev-parse", "HEAD")
	want["scope"] = []any{"src/seal", "tests/unit", ".", "a/b", "src/..hidden", "tests/unit"}
	want["checks"] = []any{
		map[string]any{"name": "second", "argv": []any{"printf", "둘째"}, "required": false},
		map[string]any{"name": "first", "argv": []any{"printf", "첫째"}, "required": true},
		map[string]any{"name": "second", "argv": []any{"printf", "둘째"}, "required": false},
	}
	createContractAssertSuccess(t, fixture.repository, createContractTaskID, result, want)

	for _, escaped := range [][]byte{[]byte(`\u003c`), []byte(`\u003e`), []byte(`\u0026`), []byte(`\u2028`), []byte(`\u2029`)} {
		if bytes.Contains(result.stdout, escaped) {
			t.Fatalf("stdout contains Python-incompatible escape %q: %s", escaped, result.stdout)
		}
	}
	for _, raw := range [][]byte{[]byte("목표"), []byte("<>&"), []byte("\u2028"), []byte("\u2029")} {
		if !bytes.Contains(result.stdout, raw) {
			t.Fatalf("stdout does not contain raw UTF-8 %q: %s", raw, result.stdout)
		}
	}
}

func TestTaskCreateContractSupportsFilesystemValidLongTaskID(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	taskID := "T" + strings.Repeat("A", 219)
	spec := createContractValidSpec()
	spec["id"] = taskID
	createContractWriteJSON(t, fixture.input, spec)

	result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	want := createContractExpectedBaseSnapshot(createContractGit(t, fixture.repository, "rev-parse", "HEAD"))
	want["id"] = taskID
	createContractAssertSuccess(t, fixture.repository, taskID, result, want)
	createContractAssertTaskDirectory(t, fixture.repository, taskID)
}

func TestTaskCreateContractRepositoryAndInputBoundaries(t *testing.T) {
	t.Run("dirty repository and outside input are accepted without mutation", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		createContractWriteRaw(t, filepath.Join(fixture.repository, "README.md"), []byte("dirty tracked file\n"))
		createContractWriteRaw(t, filepath.Join(fixture.repository, "staged.txt"), []byte("staged\n"))
		createContractGit(t, fixture.repository, "add", "staged.txt")
		createContractWriteRaw(t, filepath.Join(fixture.repository, "untracked.txt"), []byte("untracked\n"))
		outsideInput := filepath.Join(t.TempDir(), "outside-task.json")
		createContractWriteJSON(t, outsideInput, createContractValidSpec())
		inputBefore := createContractReadFile(t, outsideInput)
		catalogBefore := createContractReadFile(t, createContractCatalogPath(fixture.repository))
		statusBefore := createContractGitRaw(t, fixture.repository, "status", "--porcelain=v1", "-z")
		headBefore := createContractGit(t, fixture.repository, "rev-parse", "HEAD")

		result := createContractRun(t, fixture.repository, "task", "create", "--file", outsideInput)
		want := createContractExpectedBaseSnapshot(headBefore)
		createContractAssertSuccess(t, fixture.repository, createContractTaskID, result, want)

		if got := createContractGitRaw(t, fixture.repository, "status", "--porcelain=v1", "-z"); got != statusBefore {
			t.Fatalf("git status changed\ngot:  %q\nwant: %q", got, statusBefore)
		}
		if got := createContractGit(t, fixture.repository, "rev-parse", "HEAD"); got != headBefore {
			t.Fatalf("HEAD = %q, want %q", got, headBefore)
		}
		createContractRequireBytesEqual(t, outsideInput, inputBefore)
		createContractRequireBytesEqual(t, createContractCatalogPath(fixture.repository), catalogBefore)
	})

	t.Run("nested cwd resolves a relative input against that cwd", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		nested := filepath.Join(fixture.repository, "nested", "work")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", nested, err)
		}
		input := filepath.Join(nested, "task.json")
		createContractWriteJSON(t, input, createContractValidSpec())
		result := createContractRun(t, nested, "task", "create", "--file", "task.json")
		createContractAssertSuccess(
			t,
			fixture.repository,
			createContractTaskID,
			result,
			createContractExpectedBaseSnapshot(createContractGit(t, fixture.repository, "rev-parse", "HEAD")),
		)
	})

	t.Run("ordinary external input and catalog symlinks remain readable", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		outside := t.TempDir()
		externalInput := filepath.Join(outside, "task.json")
		externalCatalog := filepath.Join(outside, "checks.json")
		createContractWriteJSON(t, externalInput, createContractValidSpec())
		createContractWriteJSON(t, externalCatalog, createContractValidCatalog())
		if err := os.Remove(fixture.input); err != nil {
			t.Fatalf("Remove input: %v", err)
		}
		if err := os.Remove(createContractCatalogPath(fixture.repository)); err != nil {
			t.Fatalf("Remove catalog: %v", err)
		}
		createContractSymlink(t, externalInput, fixture.input)
		createContractSymlink(t, externalCatalog, createContractCatalogPath(fixture.repository))
		inputBefore := createContractReadFile(t, externalInput)
		catalogBefore := createContractReadFile(t, externalCatalog)

		result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
		createContractAssertSuccess(
			t,
			fixture.repository,
			createContractTaskID,
			result,
			createContractExpectedBaseSnapshot(createContractGit(t, fixture.repository, "rev-parse", "HEAD")),
		)
		createContractRequireBytesEqual(t, externalInput, inputBefore)
		createContractRequireBytesEqual(t, externalCatalog, catalogBefore)
	})
}

func TestTaskCreateContractRejectsInvalidInputJSONWithoutWrites(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T, createContractFixture) string
		wantPrefix string
	}{
		{
			name: "missing input",
			prepare: func(t *testing.T, fixture createContractFixture) string {
				return filepath.Join(fixture.repository, "missing-task.json")
			},
			wantPrefix: "Task Spec file does not exist:",
		},
		{
			name: "malformed input",
			prepare: func(t *testing.T, fixture createContractFixture) string {
				createContractWriteRaw(t, fixture.input, []byte("{\n"))
				return fixture.input
			},
			wantPrefix: "Task Spec is not valid JSON:",
		},
		{
			name: "non-object input",
			prepare: func(t *testing.T, fixture createContractFixture) string {
				createContractWriteRaw(t, fixture.input, []byte("[]\n"))
				return fixture.input
			},
			wantPrefix: "Task Spec must be a JSON object.",
		},
		{
			name: "trailing JSON value",
			prepare: func(t *testing.T, fixture createContractFixture) string {
				createContractWriteRaw(t, fixture.input, []byte("{} {}\n"))
				return fixture.input
			},
			wantPrefix: "Task Spec is not valid JSON:",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createContractNewFixture(t, true)
			input := test.prepare(t, fixture)
			before := createContractSnapshotTree(t, fixture.repository)
			result := createContractRun(t, fixture.repository, "task", "create", "--file", input)
			createContractAssertErrorPrefix(t, result, 2, test.wantPrefix)
			if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid input changed repository\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

func TestTaskCreateContractRejectsInvalidTaskFieldsWithoutWrites(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(map[string]any)
		wantMessage string
	}{
		{name: "missing field", mutate: func(spec map[string]any) { delete(spec, "verifier") }, wantMessage: "Task Spec is missing required field(s): verifier."},
		{name: "unexpected field", mutate: func(spec map[string]any) { spec["extra"] = true }, wantMessage: "Task Spec has unexpected field(s): extra."},
		{name: "schema boolean", mutate: func(spec map[string]any) { spec["schema_version"] = true }, wantMessage: "Task Spec schema_version must be 1."},
		{name: "schema lexical float", mutate: func(spec map[string]any) { spec["schema_version"] = json.Number("1.0") }, wantMessage: "Task Spec schema_version must be 1."},
		{name: "schema wrong integer", mutate: func(spec map[string]any) { spec["schema_version"] = 2 }, wantMessage: "Task Spec schema_version must be 1."},
		{name: "id non-string", mutate: func(spec map[string]any) { spec["id"] = 1 }, wantMessage: "Task id must be a non-empty string."},
		{name: "id empty", mutate: func(spec map[string]any) { spec["id"] = "" }, wantMessage: "Task id must be a non-empty string."},
		{name: "id invalid first character", mutate: func(spec map[string]any) { spec["id"] = "_TASK" }, wantMessage: "Task id must begin with an alphanumeric character and contain only letters, numbers, underscores, or hyphens."},
		{name: "id non-ASCII", mutate: func(spec map[string]any) { spec["id"] = "TASK-한글" }, wantMessage: "Task id must contain only letters, numbers, underscores, or hyphens."},
		{name: "id trailing newline accepted by Schema but rejected by Reference", mutate: func(spec map[string]any) { spec["id"] = "TASK\n" }, wantMessage: "Task id must contain only letters, numbers, underscores, or hyphens."},
		{name: "type invalid", mutate: func(spec map[string]any) { spec["type"] = "maintenance" }, wantMessage: "Task Spec type must be one of: bugfix, config-infra, docs, feature, refactor, test."},
		{name: "objective non-string", mutate: func(spec map[string]any) { spec["objective"] = false }, wantMessage: "Task Spec objective must be a non-empty string."},
		{name: "objective empty", mutate: func(spec map[string]any) { spec["objective"] = "" }, wantMessage: "Task Spec objective must be a non-empty string."},
		{name: "risk invalid", mutate: func(spec map[string]any) { spec["risk"] = "critical" }, wantMessage: "Task Spec risk must be one of: high, low, medium."},
		{name: "scope non-array", mutate: func(spec map[string]any) { spec["scope"] = "src" }, wantMessage: "Task Spec scope must be a non-empty array."},
		{name: "scope empty", mutate: func(spec map[string]any) { spec["scope"] = []any{} }, wantMessage: "Task Spec scope must be a non-empty array."},
		{name: "scope item empty", mutate: func(spec map[string]any) { spec["scope"] = []any{""} }, wantMessage: "Task Spec scope[0] must be a non-empty string."},
		{name: "scope POSIX absolute", mutate: func(spec map[string]any) { spec["scope"] = []any{"/tmp/outside"} }, wantMessage: "Task Spec scope[0] must be relative to the repository root."},
		{name: "scope Windows drive", mutate: func(spec map[string]any) { spec["scope"] = []any{`C:\outside`} }, wantMessage: "Task Spec scope[0] must be relative to the repository root."},
		{name: "scope non-letter Windows drive accepted by Schema but rejected by Reference", mutate: func(spec map[string]any) { spec["scope"] = []any{"1:outside"} }, wantMessage: "Task Spec scope[0] must be relative to the repository root."},
		{name: "scope traversal", mutate: func(spec map[string]any) { spec["scope"] = []any{"src/../outside"} }, wantMessage: "Task Spec scope[0] must not contain '..' traversal."},
		{name: "checks non-array", mutate: func(spec map[string]any) { spec["checks"] = "unit" }, wantMessage: "Task Spec checks must be a non-empty array."},
		{name: "checks empty", mutate: func(spec map[string]any) { spec["checks"] = []any{} }, wantMessage: "Task Spec checks must be a non-empty array."},
		{name: "check empty reference", mutate: func(spec map[string]any) { spec["checks"] = []any{""} }, wantMessage: "Task Spec checks[0] must be a non-empty string."},
		{name: "check unknown reference", mutate: func(spec map[string]any) { spec["checks"] = []any{"missing"} }, wantMessage: "Task Spec checks[0] references unknown catalog check 'missing'."},
		{name: "inline check non-object", mutate: func(spec map[string]any) { spec["checks"] = []any{1} }, wantMessage: "Task Spec checks[0] must be a JSON object."},
		{name: "inline check missing name", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"argv": []any{"true"}, "required": true}}
		}, wantMessage: "Task Spec checks[0] is missing required field(s): name."},
		{name: "inline check unexpected field", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{"true"}, "required": true, "extra": true}}
		}, wantMessage: "Task Spec checks[0] has unexpected field(s): extra."},
		{name: "inline argv string", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": "true", "required": true}}
		}, wantMessage: "Task Spec checks[0] argv must be a non-empty array."},
		{name: "inline argv empty", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{}, "required": true}}
		}, wantMessage: "Task Spec checks[0] argv must be a non-empty array."},
		{name: "inline argv empty argument", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{""}, "required": true}}
		}, wantMessage: "Task Spec checks[0] argv[0] must be a non-empty string."},
		{name: "inline required integer", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{"true"}, "required": 1}}
		}, wantMessage: "Task Spec checks[0] required must be a boolean."},
		{name: "inline timeout boolean", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{"true"}, "required": true, "timeout_seconds": true}}
		}, wantMessage: "Task Spec checks[0] timeout_seconds must be a positive integer."},
		{name: "inline timeout lexical float", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{"true"}, "required": true, "timeout_seconds": json.Number("1.0")}}
		}, wantMessage: "Task Spec checks[0] timeout_seconds must be a positive integer."},
		{name: "inline timeout zero", mutate: func(spec map[string]any) {
			spec["checks"] = []any{map[string]any{"name": "x", "argv": []any{"true"}, "required": true, "timeout_seconds": 0}}
		}, wantMessage: "Task Spec checks[0] timeout_seconds must be a positive integer."},
		{name: "verifier non-object", mutate: func(spec map[string]any) { spec["verifier"] = true }, wantMessage: "Task Spec verifier must be a JSON object."},
		{name: "verifier missing required", mutate: func(spec map[string]any) { spec["verifier"] = map[string]any{} }, wantMessage: "Task Spec verifier is missing required field(s): required."},
		{name: "verifier unexpected field", mutate: func(spec map[string]any) { spec["verifier"] = map[string]any{"required": false, "extra": true} }, wantMessage: "Task Spec verifier has unexpected field(s): extra."},
		{name: "verifier required integer", mutate: func(spec map[string]any) { spec["verifier"] = map[string]any{"required": 0} }, wantMessage: "Task Spec verifier required must be a boolean."},
		{name: "verifier preferred runner empty", mutate: func(spec map[string]any) { spec["verifier"] = map[string]any{"required": true, "preferred_runner": ""} }, wantMessage: "Task Spec verifier preferred_runner must be a non-empty string."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createContractNewFixture(t, true)
			spec := createContractValidSpec()
			test.mutate(spec)
			createContractWriteJSON(t, fixture.input, spec)
			before := createContractSnapshotTree(t, fixture.repository)
			result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
			createContractAssertExactError(t, result, 2, test.wantMessage)
			if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid Task changed repository\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

func TestTaskCreateContractValidatesWholeCatalogBeforeTaskResolution(t *testing.T) {
	tests := []struct {
		name        string
		catalog     any
		catalogRaw  string
		remove      bool
		wantMessage string
		wantPrefix  string
	}{
		{name: "missing catalog", remove: true, wantMessage: "Check catalog is missing: .seal/checks.json."},
		{name: "malformed catalog", catalogRaw: "{\n", wantPrefix: "Check catalog is not valid JSON:"},
		{name: "non-object catalog", catalog: []any{}, wantMessage: "Check catalog must be a JSON object."},
		{name: "unexpected top-level field", catalog: map[string]any{"checks": []any{}, "extra": true}, wantMessage: "Check catalog has unexpected field(s): extra."},
		{name: "schema boolean", catalog: map[string]any{"schema_version": true, "checks": []any{}}, wantMessage: "Check catalog schema_version must be 1."},
		{name: "schema lexical float", catalog: map[string]any{"schema_version": json.Number("1.0"), "checks": []any{}}, wantMessage: "Check catalog schema_version must be 1."},
		{name: "schema wrong integer", catalog: map[string]any{"schema_version": 2, "checks": []any{}}, wantMessage: "Check catalog schema_version must be 1."},
		{name: "missing checks", catalog: map[string]any{"schema_version": 1}, wantMessage: "Check catalog is missing required field: checks."},
		{name: "invalid checks type", catalog: map[string]any{"checks": true}, wantMessage: "Check catalog checks must be an array or object."},
		{name: "duplicate list name", catalog: map[string]any{"checks": []any{createContractCheck("same"), createContractCheck("same")}}, wantMessage: "Check catalog defines 'same' more than once."},
		{name: "object value non-object", catalog: map[string]any{"checks": map[string]any{"unit": true}}, wantMessage: "Check catalog entry 'unit' must be a JSON object."},
		{name: "object key name mismatch", catalog: map[string]any{"checks": map[string]any{"unit": map[string]any{"name": "other", "argv": []any{"true"}, "required": true}}}, wantMessage: "Check catalog entry 'unit' has a different name field."},
		{name: "invalid argv", catalog: map[string]any{"checks": []any{map[string]any{"name": "unit", "argv": []any{""}, "required": true}}}, wantMessage: "Check catalog checks[0] argv[0] must be a non-empty string."},
		{name: "invalid required", catalog: map[string]any{"checks": []any{map[string]any{"name": "unit", "argv": []any{"true"}, "required": 1}}}, wantMessage: "Check catalog checks[0] required must be a boolean."},
		{name: "invalid timeout", catalog: map[string]any{"checks": []any{map[string]any{"name": "unit", "argv": []any{"true"}, "required": true, "timeout_seconds": false}}}, wantMessage: "Check catalog checks[0] timeout_seconds must be a positive integer."},
		{name: "unused invalid entry still fails", catalog: map[string]any{"checks": []any{createContractCheck("unit"), map[string]any{"name": "unused", "argv": []any{}, "required": true}}}, wantMessage: "Check catalog checks[1] argv must be a non-empty array."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createContractNewFixture(t, true)
			catalogPath := createContractCatalogPath(fixture.repository)
			switch {
			case test.remove:
				if err := os.Remove(catalogPath); err != nil {
					t.Fatalf("Remove(%q): %v", catalogPath, err)
				}
			case test.catalogRaw != "":
				createContractWriteRaw(t, catalogPath, []byte(test.catalogRaw))
			default:
				createContractWriteJSON(t, catalogPath, test.catalog)
			}
			before := createContractSnapshotTree(t, fixture.repository)
			result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
			if test.wantPrefix != "" {
				createContractAssertErrorPrefix(t, result, 2, test.wantPrefix)
			} else {
				createContractAssertExactError(t, result, 2, test.wantMessage)
			}
			if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid catalog changed repository\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

func TestTaskCreateContractFailurePrecedence(t *testing.T) {
	t.Run("repository resolution precedes input read", func(t *testing.T) {
		cwd := t.TempDir()
		missing := filepath.Join(cwd, "missing.json")
		result := createContractRun(t, cwd, "task", "create", "--file", missing)
		createContractAssertExactError(t, result, 3, "Task commands must run inside a Git repository.")
	})

	t.Run("repository resolution precedes empty input path", func(t *testing.T) {
		cwd := t.TempDir()
		result := createContractRun(t, cwd, "task", "create", "--file=")
		createContractAssertExactError(t, result, 3, "Task commands must run inside a Git repository.")
	})

	t.Run("empty input path is a handled input error inside repository", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		before := createContractSnapshotTree(t, fixture.repository)
		result := createContractRun(t, fixture.repository, "task", "create", "--file=")
		createContractAssertHandledError(t, result, 2)
		if strings.Contains(result.stderr, "usage:") {
			t.Fatalf("empty input path was rejected by CLI parsing: %q", result.stderr)
		}
		if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("empty input failure changed repository\nbefore: %#v\nafter: %#v", before, after)
		}
	})

	t.Run("input read precedes catalog validation", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		missing := filepath.Join(fixture.repository, "missing.json")
		createContractWriteRaw(t, createContractCatalogPath(fixture.repository), []byte("{\n"))
		result := createContractRun(t, fixture.repository, "task", "create", "--file", missing)
		createContractAssertErrorPrefix(t, result, 2, "Task Spec file does not exist:")
	})

	t.Run("catalog validation precedes Task validation", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		spec := createContractValidSpec()
		spec["type"] = "invalid"
		createContractWriteJSON(t, fixture.input, spec)
		createContractWriteJSON(t, createContractCatalogPath(fixture.repository), map[string]any{"checks": true})
		result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
		createContractAssertExactError(t, result, 2, "Check catalog checks must be an array or object.")
	})

	t.Run("Task validation precedes current HEAD", func(t *testing.T) {
		fixture := createContractNewFixture(t, false)
		spec := createContractValidSpec()
		spec["risk"] = "invalid"
		createContractWriteJSON(t, fixture.input, spec)
		result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
		createContractAssertExactError(t, result, 2, "Task Spec risk must be one of: high, low, medium.")
	})

	t.Run("current HEAD precedes duplicate destination", func(t *testing.T) {
		fixture := createContractNewFixture(t, false)
		destination := createContractTaskPath(fixture.repository, createContractTaskID)
		createContractWriteRaw(t, destination, []byte("unchanged\n"))
		before := createContractSnapshotTree(t, fixture.repository)
		result := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
		createContractAssertExactError(t, result, 3, "Task creation requires a repository with a current HEAD.")
		if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
			t.Fatalf("HEAD failure changed repository\nbefore: %#v\nafter: %#v", before, after)
		}
	})
}

func TestTaskCreateContractStorageForceAndExactWriteSet(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	inputBefore := createContractReadFile(t, fixture.input)
	catalogBefore := createContractReadFile(t, createContractCatalogPath(fixture.repository))
	before := createContractSnapshotTree(t, fixture.repository)
	baseline := createContractGit(t, fixture.repository, "rev-parse", "HEAD")

	first := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	createContractAssertSuccess(t, fixture.repository, createContractTaskID, first, createContractExpectedBaseSnapshot(baseline))
	afterFirst := createContractSnapshotTree(t, fixture.repository)
	if got, want := createContractChangedPaths(before, afterFirst), []string{".seal/tasks", ".seal/tasks/" + createContractTaskID + ".json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first create changed paths = %q, want %q", got, want)
	}
	createContractAssertTaskDirectory(t, fixture.repository, createContractTaskID)
	createContractRequireBytesEqual(t, fixture.input, inputBefore)
	createContractRequireBytesEqual(t, createContractCatalogPath(fixture.repository), catalogBefore)

	destination := createContractTaskPath(fixture.repository, createContractTaskID)
	destinationBefore := createContractReadFile(t, destination)
	beforeDuplicate := createContractSnapshotTree(t, fixture.repository)
	duplicate := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input)
	createContractAssertExactError(t, duplicate, 2, "Task '"+createContractTaskID+"' already exists; use --force to replace it.")
	if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, beforeDuplicate) {
		t.Fatalf("no-force duplicate changed repository\nbefore: %#v\nafter: %#v", beforeDuplicate, after)
	}
	createContractRequireBytesEqual(t, destination, destinationBefore)
	createContractAssertTaskDirectory(t, fixture.repository, createContractTaskID)

	createContractWriteRaw(t, filepath.Join(fixture.repository, "HEAD-CHANGE.txt"), []byte("new head\n"))
	createContractGit(t, fixture.repository, "add", "HEAD-CHANGE.txt")
	createContractGit(t, fixture.repository, "-c", "user.name=Seal Go Contract", "-c", "user.email=seal-go@example.invalid", "commit", "--quiet", "-m", "new head")
	replacement := createContractValidSpec()
	replacement["objective"] = "replacement objective"
	createContractWriteJSON(t, fixture.input, replacement)
	inputReplacement := createContractReadFile(t, fixture.input)
	beforeForce := createContractSnapshotTree(t, fixture.repository)
	forceBaseline := createContractGit(t, fixture.repository, "rev-parse", "HEAD")
	forced := createContractRun(t, fixture.repository, "task", "create", "--file", fixture.input, "--force")
	wantForced := createContractExpectedBaseSnapshot(forceBaseline)
	wantForced["objective"] = "replacement objective"
	createContractAssertSuccess(t, fixture.repository, createContractTaskID, forced, wantForced)
	if got, want := createContractChangedPaths(beforeForce, createContractSnapshotTree(t, fixture.repository)), []string{".seal/tasks/" + createContractTaskID + ".json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("force changed paths = %q, want %q", got, want)
	}
	createContractRequireBytesEqual(t, fixture.input, inputReplacement)
	createContractAssertTaskDirectory(t, fixture.repository, createContractTaskID)
}

func TestTaskCreateContractNoForceRacePublishesExactlyOnce(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	start := make(chan struct{})
	results := make([]createContractResult, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index] = createContractInvoke(fixture.repository, "task", "create", "--file", fixture.input)
		}(index)
	}
	close(start)
	wait.Wait()
	for _, result := range results {
		createContractRequireSupported(t, result)
	}
	codes := []int{results[0].code, results[1].code}
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{0, 2}) {
		t.Fatalf("concurrent exit codes = %v, want [0 2]; results = %#v", codes, results)
	}
	for _, result := range results {
		if result.code == 0 {
			createContractAssertSuccess(
				t,
				fixture.repository,
				createContractTaskID,
				result,
				createContractExpectedBaseSnapshot(createContractGit(t, fixture.repository, "rev-parse", "HEAD")),
			)
		} else {
			createContractAssertExactError(t, result, 2, "Task '"+createContractTaskID+"' already exists; use --force to replace it.")
		}
	}
	createContractAssertTaskDirectory(t, fixture.repository, createContractTaskID)
}

func TestTaskCreateContractRejectsUnsafeWriterDestinationsWithoutWrites(t *testing.T) {
	tests := []struct {
		name  string
		force bool
		setup func(*testing.T, createContractFixture, string)
	}{
		{
			name: "seal symlink to internal directory",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				target := filepath.Join(fixture.repository, "metadata")
				if err := os.Rename(filepath.Join(fixture.repository, ".seal"), target); err != nil {
					t.Fatalf("Rename .seal: %v", err)
				}
				createContractSymlink(t, "metadata", filepath.Join(fixture.repository, ".seal"))
			},
		},
		{
			name: "seal symlink to external directory",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				if err := os.RemoveAll(filepath.Join(fixture.repository, ".seal")); err != nil {
					t.Fatalf("RemoveAll .seal: %v", err)
				}
				externalSeal := filepath.Join(outside, "seal")
				createContractWriteJSON(t, filepath.Join(externalSeal, "checks.json"), createContractValidCatalog())
				createContractSymlink(t, externalSeal, filepath.Join(fixture.repository, ".seal"))
			},
		},
		{
			name: "tasks symlink to internal directory",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				target := filepath.Join(fixture.repository, "stored-tasks")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatalf("MkdirAll(%q): %v", target, err)
				}
				createContractSymlink(t, filepath.Join("..", "stored-tasks"), filepath.Join(fixture.repository, ".seal", "tasks"))
			},
		},
		{
			name: "tasks symlink to external directory",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				target := filepath.Join(outside, "tasks")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatalf("MkdirAll(%q): %v", target, err)
				}
				createContractSymlink(t, target, filepath.Join(fixture.repository, ".seal", "tasks"))
			},
		},
		{
			name: "no-force destination symlink is rejected without following it",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				target := filepath.Join(outside, "target.json")
				createContractWriteRaw(t, target, []byte("external unchanged\n"))
				createContractSymlink(t, target, createContractTaskPath(fixture.repository, createContractTaskID))
			},
		},
		{
			name:  "force destination symlink to external file",
			force: true,
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				target := filepath.Join(outside, "target.json")
				createContractWriteRaw(t, target, []byte("external unchanged\n"))
				createContractSymlink(t, target, createContractTaskPath(fixture.repository, createContractTaskID))
			},
		},
		{
			name:  "force broken destination symlink",
			force: true,
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				createContractSymlink(t, filepath.Join(outside, "missing.json"), createContractTaskPath(fixture.repository, createContractTaskID))
			},
		},
		{
			name:  "force destination symlink loop",
			force: true,
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				createContractSymlink(t, createContractTaskID+".json", createContractTaskPath(fixture.repository, createContractTaskID))
			},
		},
		{
			name:  "force destination directory",
			force: true,
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				if err := os.MkdirAll(createContractTaskPath(fixture.repository, createContractTaskID), 0o755); err != nil {
					t.Fatalf("MkdirAll destination: %v", err)
				}
			},
		},
		{
			name: "seal is a non-directory",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				if err := os.RemoveAll(filepath.Join(fixture.repository, ".seal")); err != nil {
					t.Fatalf("RemoveAll .seal: %v", err)
				}
				createContractWriteRaw(t, filepath.Join(fixture.repository, ".seal"), []byte("not a directory\n"))
			},
		},
		{
			name: "tasks is a non-directory",
			setup: func(t *testing.T, fixture createContractFixture, outside string) {
				createContractWriteRaw(t, filepath.Join(fixture.repository, ".seal", "tasks"), []byte("not a directory\n"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createContractNewFixture(t, true)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", outside, err)
			}
			test.setup(t, fixture, outside)
			repositoryBefore := createContractSnapshotTree(t, fixture.repository)
			outsideBefore := createContractSnapshotTree(t, outside)
			args := []string{"task", "create", "--file", fixture.input}
			if test.force {
				args = append(args, "--force")
			}
			result := createContractRun(t, fixture.repository, args...)
			createContractAssertHandledError(t, result, 2)
			if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, repositoryBefore) {
				t.Fatalf("unsafe destination changed repository\nbefore: %#v\nafter:  %#v", repositoryBefore, after)
			}
			if after := createContractSnapshotTree(t, outside); !reflect.DeepEqual(after, outsideBefore) {
				t.Fatalf("unsafe destination changed outside tree\nbefore: %#v\nafter:  %#v", outsideBefore, after)
			}
		})
	}
}

func TestTaskCreateContractCLIShape(t *testing.T) {
	fixture := createContractNewFixture(t, true)
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing file option", args: []string{"task", "create"}},
		{name: "missing file value", args: []string{"task", "create", "--file"}},
		{name: "extra positional argument", args: []string{"task", "create", "--file", fixture.input, "extra"}},
		{name: "unsupported option", args: []string{"task", "create", "--file", fixture.input, "--latest"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := createContractSnapshotTree(t, fixture.repository)
			result := createContractRun(t, fixture.repository, test.args...)
			if result.code != 2 || len(result.stdout) != 0 || !strings.Contains(result.stderr, "error:") || !strings.Contains(result.stderr, "usage:") {
				t.Fatalf("CLI shape result = %#v, want exit 2, empty stdout, error and usage", result)
			}
			if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
				t.Fatalf("CLI error changed repository\nbefore: %#v\nafter: %#v", before, after)
			}
		})
	}
}

func TestTaskCreateContractAcceptedCLIForms(t *testing.T) {
	tests := []struct {
		name      string
		arguments func(createContractFixture) []string
	}{
		{
			name: "file option with equals",
			arguments: func(fixture createContractFixture) []string {
				return []string{"task", "create", "--file=" + fixture.input}
			},
		},
		{
			name: "force before file option",
			arguments: func(fixture createContractFixture) []string {
				return []string{"task", "create", "--force", "--file", fixture.input}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createContractNewFixture(t, true)
			result := createContractRun(t, fixture.repository, test.arguments(fixture)...)
			createContractAssertSuccess(
				t,
				fixture.repository,
				createContractTaskID,
				result,
				createContractExpectedBaseSnapshot(createContractGit(t, fixture.repository, "rev-parse", "HEAD")),
			)
		})
	}
}

func TestTaskCreateContractReferenceRuntimeFailuresWithoutWrites(t *testing.T) {
	t.Run("invalid UTF-8 input", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		createContractWriteRaw(t, fixture.input, []byte{0xff})
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			1,
			"task", "create", "--file", fixture.input,
		)
	})

	t.Run("invalid UTF-8 catalog", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		createContractWriteRaw(t, createContractCatalogPath(fixture.repository), []byte{0xff})
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			1,
			"task", "create", "--file", fixture.input,
		)
	})

	t.Run("4301-digit integer token", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		spec := createContractValidSpec()
		spec["checks"] = []any{map[string]any{
			"name":            "large",
			"argv":            []any{"true"},
			"required":        true,
			"timeout_seconds": json.Number(strings.Repeat("9", 4301)),
		}}
		createContractWriteJSON(t, fixture.input, spec)
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			1,
			"task", "create", "--file", fixture.input,
		)
	})
}

func TestTaskCreateApprovedDivergencesPreserveInputsAndDestinations(t *testing.T) {
	t.Run("input aliases destination", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		destination := createContractTaskPath(fixture.repository, createContractTaskID)
		createContractWriteJSON(t, destination, createContractValidSpec())
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			2,
			"task", "create", "--file", destination, "--force",
		)
	})

	t.Run("catalog aliases destination", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		destination := createContractTaskPath(fixture.repository, createContractTaskID)
		createContractWriteJSON(t, destination, map[string]any{"checks": []any{}})
		if err := os.Remove(createContractCatalogPath(fixture.repository)); err != nil {
			t.Fatalf("Remove catalog: %v", err)
		}
		createContractSymlink(
			t,
			filepath.Join("tasks", createContractTaskID+".json"),
			createContractCatalogPath(fixture.repository),
		)
		spec := createContractValidSpec()
		spec["checks"] = []any{createContractCheck("inline")}
		createContractWriteJSON(t, fixture.input, spec)
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			2,
			"task", "create", "--file", fixture.input, "--force",
		)
	})

	t.Run("lone surrogate leaves no first-create artifact", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		createContractWriteRaw(
			t,
			fixture.input,
			createContractLoneSurrogateSpec(t),
		)
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			1,
			"task", "create", "--file", fixture.input,
		)
	})

	t.Run("lone surrogate preserves forced destination", func(t *testing.T) {
		fixture := createContractNewFixture(t, true)
		destination := createContractTaskPath(fixture.repository, createContractTaskID)
		createContractWriteRaw(t, destination, []byte("existing snapshot bytes\n"))
		createContractWriteRaw(
			t,
			fixture.input,
			createContractLoneSurrogateSpec(t),
		)
		createContractAssertRejectedUnchanged(
			t,
			fixture,
			1,
			"task", "create", "--file", fixture.input, "--force",
		)
	})
}

func createContractLoneSurrogateSpec(t *testing.T) []byte {
	t.Helper()
	contents := createContractReferenceJSON(t, createContractValidSpec())
	return bytes.Replace(
		contents,
		[]byte("Create one deterministic Task snapshot."),
		[]byte(`before\ud800after`),
		1,
	)
}

func createContractNewFixture(t *testing.T, withHead bool) createContractFixture {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", repository, err)
	}
	createContractGit(t, repository, "init", "--quiet")
	createContractGit(t, repository, "config", "maintenance.auto", "false")
	createContractGit(t, repository, "config", "maintenance.autoDetach", "false")
	createContractGit(t, repository, "config", "gc.auto", "0")
	if withHead {
		createContractWriteRaw(t, filepath.Join(repository, "README.md"), []byte("fixture\n"))
		createContractGit(t, repository, "add", "README.md")
		createContractGit(
			t,
			repository,
			"-c", "user.name=Seal Go Contract",
			"-c", "user.email=seal-go@example.invalid",
			"commit", "--quiet", "-m", "fixture",
		)
	}
	createContractWriteJSON(t, createContractCatalogPath(repository), createContractValidCatalog())
	input := filepath.Join(repository, "task-input.json")
	createContractWriteJSON(t, input, createContractValidSpec())
	return createContractFixture{repository: repository, input: input}
}

func createContractValidSpec() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"id":             createContractTaskID,
		"type":           "feature",
		"objective":      "Create one deterministic Task snapshot.",
		"scope":          []any{"./src//seal/", `tests\unit`},
		"checks":         []any{"unit"},
		"risk":           "medium",
		"verifier":       map[string]any{"required": false},
	}
}

func createContractValidCatalog() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"checks":         []any{createContractCheck("unit")},
	}
}

func createContractCheck(name string) map[string]any {
	return map[string]any{
		"name":     name,
		"argv":     []any{"go", "test", "./..."},
		"required": true,
	}
}

func createContractExpectedBaseSnapshot(baseline string) map[string]any {
	return map[string]any{
		"baseline":       baseline,
		"checks":         []any{createContractCheck("unit")},
		"id":             createContractTaskID,
		"objective":      "Create one deterministic Task snapshot.",
		"risk":           "medium",
		"schema_version": 1,
		"scope":          []any{"src/seal", "tests/unit"},
		"type":           "feature",
		"verifier":       map[string]any{"required": false},
	}
}

func createContractCopyMap(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source)+1)
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func createContractCatalogPath(repository string) string {
	return filepath.Join(repository, ".seal", "checks.json")
}

func createContractTaskPath(repository, taskID string) string {
	return filepath.Join(repository, ".seal", "tasks", taskID+".json")
}

func createContractInvoke(cwd string, args ...string) createContractResult {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI(cwd, args, &stdout, &stderr)
	return createContractResult{
		code:   code,
		stdout: append([]byte(nil), stdout.Bytes()...),
		stderr: stderr.String(),
	}
}

func createContractRun(t *testing.T, cwd string, args ...string) createContractResult {
	t.Helper()
	result := createContractInvoke(cwd, args...)
	createContractRequireSupported(t, result)
	return result
}

func createContractRequireSupported(t *testing.T, result createContractResult) {
	t.Helper()
	if result.code == 2 && len(result.stdout) == 0 && strings.Contains(
		result.stderr,
		"expected --help, --version, task show, or run show",
	) {
		t.Fatalf("task create is not implemented: %s", result.stderr)
	}
}

func createContractAssertSuccess(
	t *testing.T,
	repository string,
	taskID string,
	result createContractResult,
	want map[string]any,
) {
	t.Helper()
	if result.code != 0 || result.stderr != "" {
		t.Fatalf("create result = code %d, stdout %q, stderr %q; want success", result.code, result.stdout, result.stderr)
	}
	wantBytes := createContractReferenceJSON(t, want)
	if !bytes.Equal(result.stdout, wantBytes) {
		t.Fatalf("stdout mismatch\ngot:\n%s\nwant:\n%s", result.stdout, wantBytes)
	}
	stored := createContractReadFile(t, createContractTaskPath(repository, taskID))
	if !bytes.Equal(stored, wantBytes) {
		t.Fatalf("stored snapshot mismatch\ngot:\n%s\nwant:\n%s", stored, wantBytes)
	}
	if !bytes.Equal(result.stdout, stored) {
		t.Fatalf("stdout and stored snapshot differ\nstdout:\n%s\nstored:\n%s", result.stdout, stored)
	}
}

func createContractAssertExactError(t *testing.T, result createContractResult, code int, message string) {
	t.Helper()
	if result.code != code || len(result.stdout) != 0 || result.stderr != "error: "+message+"\n" {
		t.Fatalf("result = code %d, stdout %q, stderr %q; want code %d and %q", result.code, result.stdout, result.stderr, code, "error: "+message+"\n")
	}
}

func createContractAssertErrorPrefix(t *testing.T, result createContractResult, code int, messagePrefix string) {
	t.Helper()
	wantPrefix := "error: " + messagePrefix
	if result.code != code || len(result.stdout) != 0 || !strings.HasPrefix(result.stderr, wantPrefix) || !strings.HasSuffix(result.stderr, "\n") {
		t.Fatalf("result = code %d, stdout %q, stderr %q; want code %d and stderr prefix %q", result.code, result.stdout, result.stderr, code, wantPrefix)
	}
}

func createContractAssertHandledError(t *testing.T, result createContractResult, code int) {
	t.Helper()
	if result.code != code || len(result.stdout) != 0 || !strings.HasPrefix(result.stderr, "error: ") || strings.Count(result.stderr, "\n") != 1 {
		t.Fatalf("result = code %d, stdout %q, stderr %q; want handled exit %d", result.code, result.stdout, result.stderr, code)
	}
}

func createContractAssertRejectedUnchanged(
	t *testing.T,
	fixture createContractFixture,
	code int,
	arguments ...string,
) {
	t.Helper()
	before := createContractSnapshotTree(t, fixture.repository)
	result := createContractInvoke(fixture.repository, arguments...)
	createContractAssertHandledError(t, result, code)
	if after := createContractSnapshotTree(t, fixture.repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("task-create rejection changed repository\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func createContractAssertTaskDirectory(t *testing.T, repository, taskID string) {
	t.Helper()
	wantDirectoryMode := os.FileMode(0o755)
	wantTaskMode := os.FileMode(0o644)
	if runtime.GOOS == "windows" {
		// Windows reports synthesized permission bits for ordinary directories
		// and writable files rather than the POSIX modes supplied at creation.
		wantDirectoryMode = 0o777
		wantTaskMode = 0o666
	}
	tasks := filepath.Join(repository, ".seal", "tasks")
	entries, err := os.ReadDir(tasks)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", tasks, err)
	}
	if len(entries) != 1 || entries[0].Name() != taskID+".json" || !entries[0].Type().IsRegular() {
		t.Fatalf("Task directory entries = %#v, want only %s.json", entries, taskID)
	}
	tasksInfo, err := os.Stat(tasks)
	if err != nil {
		t.Fatalf("Stat(%q): %v", tasks, err)
	}
	if got := tasksInfo.Mode().Perm(); got != wantDirectoryMode {
		t.Fatalf("Task directory mode = %o, want %o", got, wantDirectoryMode)
	}
	taskInfo, err := os.Stat(createContractTaskPath(repository, taskID))
	if err != nil {
		t.Fatalf("Stat Task: %v", err)
	}
	if got := taskInfo.Mode().Perm(); got != wantTaskMode {
		t.Fatalf("Task mode = %o, want %o", got, wantTaskMode)
	}
}

func createContractReferenceJSON(t *testing.T, value any) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("Encode Reference JSON: %v", err)
	}
	contents := output.Bytes()
	contents = bytes.ReplaceAll(contents, []byte(`\u2028`), []byte("\u2028"))
	contents = bytes.ReplaceAll(contents, []byte(`\u2029`), []byte("\u2029"))
	return append([]byte(nil), contents...)
}

func createContractWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	createContractWriteRaw(t, path, createContractReferenceJSON(t, value))
}

func createContractWriteRaw(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func createContractReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return contents
}

func createContractRequireBytesEqual(t *testing.T, path string, want []byte) {
	t.Helper()
	if got := createContractReadFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("%s changed\ngot:  % x\nwant: % x", path, got, want)
	}
}

func createContractGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(createContractGitRaw(t, repository, args...))
}

func createContractGitRaw(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func createContractSnapshotTree(t *testing.T, root string) createContractTree {
	t.Helper()
	snapshot := make(createContractTree)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[key] = fmt.Sprintf("symlink:%o:%s", info.Mode().Perm(), target)
		case info.IsDir():
			snapshot[key] = fmt.Sprintf("directory:%o", info.Mode().Perm())
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[key] = fmt.Sprintf("file:%o:%x", info.Mode().Perm(), contents)
		default:
			snapshot[key] = fmt.Sprintf("other:%s", info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %q: %v", root, err)
	}
	return snapshot
}

func createContractChangedPaths(before, after createContractTree) []string {
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	changed := make([]string, 0)
	for key := range keys {
		if before[key] != after[key] {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

func createContractSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, path, err)
	}
}
