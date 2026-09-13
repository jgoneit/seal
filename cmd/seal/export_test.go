package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunCLIExportUsesPublicEnvelopeWithoutWrites(t *testing.T) {
	repository := conformanceRepository(t)
	before := snapshotTestTree(t, repository)
	var stdout, stderr bytes.Buffer
	code := runCLI(repository, []string{"run", "export", "--format", "json"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("export code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	result := decodeSingleJSON(t, stdout.Bytes()).(map[string]any)
	if len(result) != 6 || result["schema"] != "seal-run-export/v1" || result["exporter_version"] != version || result["scan_complete"] != true {
		t.Fatalf("export envelope = %#v", result)
	}
	runs := result["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["task_id"] != fixtureTaskID {
		t.Fatalf("export runs = %#v", runs)
	}
	var repeated bytes.Buffer
	code = runCLI(repository, []string{"run", "export", "--format", "json"}, &repeated, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("repeated export code=%d stderr=%q", code, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), repeated.Bytes()) {
		t.Fatalf("unchanged repository produced different export JSON:\n%s\n%s", stdout.Bytes(), repeated.Bytes())
	}
	if !reflect.DeepEqual(before, snapshotTestTree(t, repository)) {
		t.Fatal("export wrote repository state")
	}
}

func TestRunCLIExportPartialReturnsJSONAndExitEight(t *testing.T) {
	repository := conformanceRepository(t)
	if err := os.Remove(filepath.Join(repository, ".seal", "evidence", fixtureTaskID, fixtureRunID, "diff.patch")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(repository, []string{"run", "export", "--format=json"}, &stdout, &stderr)
	if code != 8 || stderr.Len() != 0 {
		t.Fatalf("partial code=%d stderr=%q", code, stderr.String())
	}
	result := decodeSingleJSON(t, stdout.Bytes()).(map[string]any)
	if result["scan_complete"] != false || len(result["issues"].([]any)) != 1 || len(result["runs"].([]any)) != 0 {
		t.Fatalf("partial = %#v", result)
	}
	if strings.Contains(stdout.String(), "diff.patch") || strings.Contains(stdout.String(), repository) {
		t.Fatal("export issue contains raw path")
	}
}

func TestRunCLIExportInputAndOutputFailures(t *testing.T) {
	for _, args := range [][]string{
		{"run", "export"}, {"run", "export", "--format", "text"},
		{"run", "export", "--format", "json", "--latest"},
		{"run", "export", "--format", "json", "--format", "json"},
		{"run", "export", fixtureTaskID, "--format", "json"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runCLI(t.TempDir(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("args %v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runCLI(t.TempDir(), []string{"run", "export", "--format", "json"}, &stdout, &stderr); code != 3 || stdout.Len() != 0 {
		t.Fatalf("non-repository code=%d stdout=%q", code, stdout.String())
	}
	stderr.Reset()
	if code := exportRuns(conformanceRepository(t), []string{"--format", "json"}, failingOutput{}, &stderr); code != 1 {
		t.Fatalf("write failure code=%d", code)
	}
}

func TestRunExportHelpRequiresNoRepository(t *testing.T) {
	args := []string{"run", "export", "--help"}
	if !isInformationalCommand(args) {
		t.Fatal("export help must not require cwd")
	}
	var stdout, stderr bytes.Buffer
	if code := runCLI("", args, &stdout, &stderr); code != 0 || stdout.String() != exportHelp || stderr.Len() != 0 {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
