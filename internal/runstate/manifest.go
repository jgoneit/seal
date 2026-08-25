package runstate

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

type manifestRecord struct {
	path      string
	sizeBytes json.Number
	sha256    string
	document  map[string]any
}

func validateManifest(runDirectory, taskID, runID string, expectedFiles []string) (string, error) {
	return validateManifestContext(context.Background(), runDirectory, taskID, runID, expectedFiles)
}

func validateManifestContext(ctx context.Context, runDirectory, taskID, runID string, expectedFiles []string) (string, error) {
	contents, err := readArtifactContext(ctx, runDirectory, "run-manifest.json")
	if err != nil {
		if _, ok := err.(*EvidenceError); !ok {
			return "", err
		}
		return "", &EvidenceError{message: fmt.Sprintf("run-manifest.json is missing or unreadable for Evidence Run '%s'.", runDirectory)}
	}
	document, err := decodeJSONObject(contents)
	if err != nil {
		if KindOf(err) == KindRuntime {
			return "", err
		}
		return "", &EvidenceError{message: filepathDisplay(runDirectory, "run-manifest.json") + " is not valid JSON."}
	}
	if !exactKeys(map[string]any(document), "schema_version", "task_id", "run_id", "files", "evidence_sha256", "created_at") {
		return "", &EvidenceError{message: "run-manifest.json has missing or unexpected field(s)."}
	}
	if !integerEquals(document["schema_version"], 1) {
		return "", &EvidenceError{message: "run-manifest.json has an unsupported schema_version."}
	}
	for _, field := range []string{"task_id", "run_id", "created_at"} {
		if value, ok := document[field].(string); !ok || value == "" {
			return "", &EvidenceError{message: "run-manifest.json " + field + " must be a non-empty string."}
		}
	}
	if _, ok := document["files"].([]any); !ok {
		return "", &EvidenceError{message: "run-manifest.json files must be an array."}
	}
	recordedEvidenceSHA, ok := document["evidence_sha256"].(string)
	if !ok || !lowerSHA256(recordedEvidenceSHA) {
		return "", &EvidenceError{message: "run-manifest.json evidence_sha256 must be a SHA-256 hex value."}
	}
	if document["task_id"] != taskID {
		return "", &EvidenceError{message: "run-manifest.json task_id does not match the requested Task id."}
	}
	if document["run_id"] != runID {
		return "", &EvidenceError{message: "run-manifest.json run_id does not match the requested Run id."}
	}

	if err := artifactContextError(ctx); err != nil {
		return "", err
	}
	records, err := validateManifestRecordsContext(ctx, document["files"].([]any), expectedFiles)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if err := artifactContextError(ctx); err != nil {
			return "", err
		}
		size, digest, err := hashArtifactContext(ctx, runDirectory, record.path)
		if err != nil {
			if _, ok := err.(*EvidenceError); !ok {
				return "", err
			}
			detail := "Could not read Evidence file: "
			if evidence, ok := err.(*EvidenceError); ok {
				switch evidence.message {
				case "missing":
					detail = "Required Evidence file is missing: "
				case "unsafe":
					detail = "Evidence file is unsafe or missing: "
				}
			}
			return "", &EvidenceError{message: detail + record.path + "."}
		}
		recordedSize, _ := new(big.Int).SetString(string(record.sizeBytes), 10)
		if recordedSize.Cmp(big.NewInt(size)) != 0 {
			return "", &EvidenceError{message: "run-manifest.json size mismatch for " + record.path + "."}
		}
		if record.sha256 != digest {
			return "", &EvidenceError{message: "run-manifest.json hash mismatch for " + record.path + "."}
		}
	}

	payloadRecords := make([]any, len(records))
	for index, record := range records {
		if err := artifactContextError(ctx); err != nil {
			return "", err
		}
		payloadRecords[index] = record.document
	}
	payload := map[string]any{
		"schema_version": json.Number("1"),
		"task_id":        taskID,
		"run_id":         runID,
		"files":          payloadRecords,
	}
	canonical, err := canonicalJSON(payload, false)
	if err != nil {
		return "", &EvidenceError{message: "run-manifest.json evidence_sha256 could not be recomputed."}
	}
	computed, err := hashBytesContext(ctx, canonical)
	if err != nil {
		return "", err
	}
	if recordedEvidenceSHA != computed {
		return "", &EvidenceError{message: "run-manifest.json evidence_sha256 does not match its file records."}
	}
	return computed, nil
}

func validateManifestRecords(raw []any, expectedFiles []string) ([]manifestRecord, error) {
	return validateManifestRecordsContext(context.Background(), raw, expectedFiles)
}

func validateManifestRecordsContext(ctx context.Context, raw []any, expectedFiles []string) ([]manifestRecord, error) {
	expectedSet := make(map[string]struct{}, len(expectedFiles))
	for _, path := range expectedFiles {
		if err := artifactContextError(ctx); err != nil {
			return nil, err
		}
		expectedSet[path] = struct{}{}
	}
	records := make([]manifestRecord, len(raw))
	seen := make(map[string]struct{}, len(raw))
	paths := make([]string, len(raw))
	for index, value := range raw {
		if err := artifactContextError(ctx); err != nil {
			return nil, err
		}
		context := fmt.Sprintf("run-manifest.json files[%d]", index)
		document, ok := value.(map[string]any)
		if !ok {
			return nil, &EvidenceError{message: context + " must be an object."}
		}
		if !exactKeys(document, "path", "size_bytes", "sha256") {
			return nil, &EvidenceError{message: context + " has missing or unexpected field(s)."}
		}
		path, err := safeRunPath(document["path"], context+".path")
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, &EvidenceError{message: context + ".path duplicates '" + path + "'."}
		}
		seen[path] = struct{}{}
		size, ok := isJSONInteger(document["size_bytes"])
		if !ok || !nonNegativeInteger(size) {
			return nil, &EvidenceError{message: context + ".size_bytes must be a non-negative integer."}
		}
		digest, ok := document["sha256"].(string)
		if !ok || !lowerSHA256(digest) {
			return nil, &EvidenceError{message: context + ".sha256 must be a SHA-256 hex value."}
		}
		paths[index] = path
		records[index] = manifestRecord{path: path, sizeBytes: size, sha256: digest, document: document}
	}
	missing := make([]string, 0)
	for path := range expectedSet {
		if err := artifactContextError(ctx); err != nil {
			return nil, err
		}
		if _, ok := seen[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return nil, &EvidenceError{message: "run-manifest.json is missing " + strings.Join(missing, ", ") + "."}
	}
	unknown := make([]string, 0)
	for path := range seen {
		if err := artifactContextError(ctx); err != nil {
			return nil, err
		}
		if _, ok := expectedSet[path]; !ok {
			unknown = append(unknown, path)
		}
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		return nil, &EvidenceError{message: "run-manifest.json has unknown file record(s): " + strings.Join(unknown, ", ") + "."}
	}
	if !sort.StringsAreSorted(paths) {
		return nil, &EvidenceError{message: "run-manifest.json file records must be sorted by ascending path."}
	}
	return records, nil
}

func filepathDisplay(directory, name string) string {
	return strings.TrimRight(directory, "/") + "/" + name
}
