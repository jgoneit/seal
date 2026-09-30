package sourceobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/jgoneit/seal/internal/pyjson"
)

// buildSnapshot constructs both forms of the Source Snapshot document. The
// digest deliberately excludes snapshot_sha256 and uses Python json.dumps
// compatible compact, ASCII-only, sorted-key bytes. The artifact itself uses
// the same field ordering with two-space indentation and literal UTF-8.
func buildSnapshot(baseline string, entries []Entry) (Snapshot, []byte, error) {
	snapshot := Snapshot{
		SchemaVersion: 1,
		Baseline:      baseline,
		Entries:       cloneEntries(entries),
	}

	payload, err := encodeDocument(snapshotDocument(snapshot, false), pyjson.Options{ASCII: true})
	if err != nil {
		return Snapshot{}, nil, repositoryFailure("Could not render source snapshot JSON.", err)
	}
	digest := sha256.Sum256(payload)
	snapshot.SHA256 = hex.EncodeToString(digest[:])

	artifact, err := encodeDocument(snapshotDocument(snapshot, true), pyjson.Options{Indent: true})
	if err != nil {
		return Snapshot{}, nil, repositoryFailure("Could not render source snapshot JSON.", err)
	}
	return snapshot, append(artifact, '\n'), nil
}

// renderChangedFiles produces the exact changed-files/v1 artifact.
func renderChangedFiles(changes ChangeSet) ([]byte, error) {
	records := make([]any, 0, len(changes.Changes))
	for _, change := range changes.Changes {
		records = append(records, map[string]any{
			"in_scope":      change.InScope,
			"is_binary":     change.Binary,
			"mode_changed":  change.ModeChanged,
			"new_mode":      nullableString(change.NewMode),
			"old_mode":      nullableString(change.OldMode),
			"path":          change.Path,
			"previous_path": nullableString(change.PreviousPath),
			"source":        change.Source,
			"status":        change.Status,
		})
	}
	scope := make([]any, 0, len(changes.Scope))
	for _, path := range changes.Scope {
		scope = append(scope, path)
	}
	encoded, err := encodeDocument(map[string]any{
		"baseline":       changes.Baseline,
		"changes":        records,
		"schema_version": 1,
		"scope":          scope,
	}, pyjson.Options{Indent: true})
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func snapshotDocument(snapshot Snapshot, includeDigest bool) map[string]any {
	entries := make([]any, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		var size any
		if entry.SizeBytes != nil {
			size = *entry.SizeBytes
		}
		entries = append(entries, map[string]any{
			"mode":       nullableString(entry.Mode),
			"path":       entry.Path,
			"sha256":     nullableString(entry.SHA256),
			"size_bytes": size,
			"state":      entry.State,
		})
	}
	document := map[string]any{
		"baseline":       snapshot.Baseline,
		"entries":        entries,
		"schema_version": snapshot.SchemaVersion,
	}
	if includeDigest {
		document["snapshot_sha256"] = snapshot.SHA256
	}
	return document
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// encodeDocument renders a fixed-schema document after rejecting strings that
// are not valid UTF-8, which these artifacts never escape or repair.
func encodeDocument(document map[string]any, options pyjson.Options) ([]byte, error) {
	if err := requireUTF8(document); err != nil {
		return nil, err
	}
	return pyjson.Encode(document, options)
}

func requireUTF8(value any) error {
	switch typed := value.(type) {
	case string:
		if !utf8.ValidString(typed) {
			return fmt.Errorf("value is not valid UTF-8")
		}
	case []any:
		for _, item := range typed {
			if err := requireUTF8(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range typed {
			if err := requireUTF8(key); err != nil {
				return err
			}
			if err := requireUTF8(item); err != nil {
				return err
			}
		}
	}
	return nil
}
