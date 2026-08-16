package runstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type sourceSnapshot struct {
	baseline       string
	entries        []sourceEntry
	snapshotSHA256 string
	document       jsonObject
}

type sourceEntry struct {
	path      string
	state     string
	mode      *string
	sizeBytes *json.Number
	sha256    *string
}

func validateSourceBinding(
	runDirectory string,
	taskBaseline string,
	changedFiles jsonObject,
	verification jsonObject,
) (bool, error) {
	before, err := readSourceSnapshot(runDirectory, "source-before-checks.json")
	if err != nil {
		return false, err
	}
	after, err := readSourceSnapshot(runDirectory, "source-after-checks.json")
	if err != nil {
		return false, err
	}
	if !integerEquals(verification["source_snapshot_schema_version"], 1) {
		return false, &EvidenceError{message: "verification.json source_snapshot_schema_version does not match the supported Source Snapshot schema."}
	}

	namedBaselines := []struct {
		name  string
		value any
	}{
		{"saved Task", taskBaseline},
		{"changed-files.json", changedFiles["baseline"]},
		{"verification.json", verification["baseline"]},
		{"source-before-checks.json", before.baseline},
		{"source-after-checks.json", after.baseline},
	}
	for _, named := range namedBaselines {
		value, ok := named.value.(string)
		if !ok || !fullObjectID(value) {
			return false, &EvidenceError{message: named.name + " baseline must be a full Git commit object id."}
		}
	}
	for _, named := range namedBaselines[1:] {
		if named.value != taskBaseline {
			return false, &EvidenceError{message: "Source-bound Evidence baselines do not match the saved Task baseline."}
		}
	}

	for _, digest := range []struct {
		field    string
		recorded any
		computed string
		filename string
	}{
		{"source_before_checks_sha256", verification["source_before_checks_sha256"], before.snapshotSHA256, "source-before-checks.json"},
		{"source_after_checks_sha256", verification["source_after_checks_sha256"], after.snapshotSHA256, "source-after-checks.json"},
	} {
		recorded, ok := digest.recorded.(string)
		if !ok || !lowerSHA256(recorded) {
			return false, &EvidenceError{message: "verification.json " + digest.field + " must be a lowercase SHA-256 digest."}
		}
		if recorded != digest.computed {
			return false, &EvidenceError{message: "verification.json " + digest.field + " does not match " + digest.filename + "."}
		}
	}

	recordedStable, ok := verification["source_stable_during_checks"].(bool)
	if !ok {
		return false, &EvidenceError{message: "verification.json source_stable_during_checks must be a boolean."}
	}
	computedStable := jsonEqual(map[string]any(before.document), map[string]any(after.document))
	if recordedStable != computedStable {
		return false, &EvidenceError{message: "verification.json source_stable_during_checks does not match the persisted pre-check and post-check Source Snapshots."}
	}
	return computedStable, nil
}

func readSourceSnapshot(runDirectory, filename string) (sourceSnapshot, error) {
	contents, err := readArtifact(runDirectory, filename)
	if err != nil {
		detail := "could not be read"
		if evidence, ok := err.(*EvidenceError); ok {
			switch evidence.message {
			case "missing":
				detail = "is missing"
			case "unsafe":
				detail = "is unsafe or missing"
			}
		}
		return sourceSnapshot{}, &EvidenceError{message: fmt.Sprintf("Required Source Binding artifact '%s' %s.", filename, detail)}
	}
	document, err := decodeJSONObject(contents)
	if err != nil {
		if KindOf(err) == KindRuntime {
			return sourceSnapshot{}, err
		}
		return sourceSnapshot{}, &EvidenceError{message: fmt.Sprintf("Source Binding artifact '%s' is not valid JSON.", filename)}
	}
	parsed, err := parseSourceSnapshot(document)
	if err != nil {
		return sourceSnapshot{}, &EvidenceError{message: fmt.Sprintf("Source Binding artifact '%s' is invalid: %s", filename, err.Error())}
	}
	return parsed, nil
}

func parseSourceSnapshot(document jsonObject) (sourceSnapshot, error) {
	if !exactKeys(map[string]any(document), "schema_version", "baseline", "entries", "snapshot_sha256") {
		return sourceSnapshot{}, fmt.Errorf("Source Snapshot document has missing or unexpected fields.")
	}
	if !integerEquals(document["schema_version"], 1) {
		return sourceSnapshot{}, fmt.Errorf("Source Snapshot document has an unsupported schema_version.")
	}
	baseline, ok := document["baseline"].(string)
	if !ok || baseline == "" {
		return sourceSnapshot{}, fmt.Errorf("Source Snapshot baseline must be a non-empty string.")
	}
	rawEntries, ok := document["entries"].([]any)
	if !ok {
		return sourceSnapshot{}, fmt.Errorf("Source Snapshot entries must be an array.")
	}
	entries := make([]sourceEntry, len(rawEntries))
	var previousSortKey []byte
	for index, value := range rawEntries {
		entry, ok := value.(map[string]any)
		if !ok {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d must be an object.", index)
		}
		if !exactKeys(entry, "path", "state", "mode", "size_bytes", "sha256") {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d has missing or unexpected fields.", index)
		}
		path, ok := entry["path"].(string)
		if !ok {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d path must be a string.", index)
		}
		if err := validateSourcePath(path); err != nil {
			return sourceSnapshot{}, err
		}
		if isMetadataPath(path) {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d contains Seal Legacy metadata.", index)
		}
		sortKey, err := sourcePathSortKey(path)
		if err != nil {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d path is not a supported Git path.", index)
		}
		if index != 0 && bytes.Compare(sortKey, previousSortKey) <= 0 {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entries must be uniquely sorted by path.")
		}
		previousSortKey = sortKey

		state, ok := entry["state"].(string)
		if !ok {
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d state is invalid.", index)
		}
		parsed := sourceEntry{path: path, state: state}
		switch state {
		case "present":
			mode, ok := entry["mode"].(string)
			if !ok || mode != "100644" && mode != "100755" && mode != "120000" {
				return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d has an unsupported mode.", index)
			}
			size, ok := isJSONInteger(entry["size_bytes"])
			if !ok || !nonNegativeInteger(size) {
				return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d size_bytes must be non-negative.", index)
			}
			digest, ok := entry["sha256"].(string)
			if !ok || !lowerSHA256(digest) {
				return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d sha256 is invalid.", index)
			}
			parsed.mode = &mode
			parsed.sizeBytes = &size
			parsed.sha256 = &digest
		case "deleted":
			if entry["mode"] != nil || entry["size_bytes"] != nil || entry["sha256"] != nil {
				return sourceSnapshot{}, fmt.Errorf("Deleted Source Snapshot entry %d must use null metadata.", index)
			}
		default:
			return sourceSnapshot{}, fmt.Errorf("Source Snapshot entry %d state is invalid.", index)
		}
		entries[index] = parsed
	}

	recordedDigest, ok := document["snapshot_sha256"].(string)
	if !ok || !lowerSHA256(recordedDigest) {
		return sourceSnapshot{}, fmt.Errorf("Source Snapshot snapshot_sha256 is invalid.")
	}
	payloadEntries := make([]any, len(rawEntries))
	for index, entry := range rawEntries {
		payloadEntries[index] = entry
	}
	payload := map[string]any{
		"schema_version": json.Number("1"),
		"baseline":       baseline,
		"entries":        payloadEntries,
	}
	canonical, err := canonicalJSON(payload, true)
	if err != nil {
		return sourceSnapshot{}, err
	}
	digest := sha256.Sum256(canonical)
	expectedDigest := hex.EncodeToString(digest[:])
	if recordedDigest != expectedDigest {
		return sourceSnapshot{}, fmt.Errorf("Source Snapshot snapshot_sha256 does not match its contents.")
	}
	return sourceSnapshot{
		baseline:       baseline,
		entries:        entries,
		snapshotSHA256: recordedDigest,
		document:       document,
	}, nil
}

func validateSourcePath(path string) error {
	if path == "" || strings.ContainsRune(path, 0) || strings.HasPrefix(path, "/") {
		return fmt.Errorf("Git source path must be repository-relative and portable: %q.", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("Git source path is not canonical and portable: %q.", path)
		}
	}
	return nil
}

func sourcePathSortKey(path string) ([]byte, error) {
	key := make([]byte, 0, len(path))
	for index := 0; index < len(path); {
		if unit, width, ok := encodedSurrogate(path[index:]); ok {
			if unit < 0xdc80 || unit > 0xdcff {
				return nil, fmt.Errorf("unsupported surrogate")
			}
			key = append(key, byte(unit-0xdc00))
			index += width
			continue
		}
		_, width := utf8.DecodeRuneInString(path[index:])
		if width == 1 && path[index] >= utf8.RuneSelf {
			return nil, fmt.Errorf("invalid UTF-8")
		}
		key = append(key, path[index:index+width]...)
		index += width
	}
	return key, nil
}

func fullObjectID(value string) bool {
	return (len(value) == 40 || len(value) == 64) && lowerHex(value)
}

func lowerSHA256(value string) bool {
	return len(value) == 64 && lowerHex(value)
}

func lowerHex(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
