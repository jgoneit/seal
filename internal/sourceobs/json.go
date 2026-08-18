package sourceobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"unicode/utf8"
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

	payload, err := renderSnapshot(snapshot, false, false, true)
	if err != nil {
		return Snapshot{}, nil, repositoryFailure("Could not render source snapshot JSON.", err)
	}
	digest := sha256.Sum256(payload)
	snapshot.SHA256 = hex.EncodeToString(digest[:])

	artifact, err := renderSnapshot(snapshot, true, true, false)
	if err != nil {
		return Snapshot{}, nil, repositoryFailure("Could not render source snapshot JSON.", err)
	}
	return snapshot, artifact, nil
}

// renderChangedFiles produces the exact changed-files/v1 artifact.
func renderChangedFiles(changes ChangeSet) ([]byte, error) {
	writer := jsonWriter{pretty: true}
	writer.beginObject()

	if err := writer.member("baseline", 0); err != nil {
		return nil, err
	}
	if err := writer.string(changes.Baseline); err != nil {
		return nil, fmt.Errorf("changed-files baseline: %w", err)
	}

	if err := writer.member("changes", 1); err != nil {
		return nil, err
	}
	writer.beginArray()
	for index, change := range changes.Changes {
		writer.element(index)
		writer.beginObject()

		if err := writer.member("in_scope", 0); err != nil {
			return nil, err
		}
		writer.boolean(change.InScope)
		if err := writer.member("is_binary", 1); err != nil {
			return nil, err
		}
		writer.boolean(change.Binary)
		if err := writer.member("mode_changed", 2); err != nil {
			return nil, err
		}
		writer.boolean(change.ModeChanged)
		if err := writer.member("new_mode", 3); err != nil {
			return nil, err
		}
		if err := writer.nullableString(change.NewMode); err != nil {
			return nil, fmt.Errorf("changed-files changes[%d].new_mode: %w", index, err)
		}
		if err := writer.member("old_mode", 4); err != nil {
			return nil, err
		}
		if err := writer.nullableString(change.OldMode); err != nil {
			return nil, fmt.Errorf("changed-files changes[%d].old_mode: %w", index, err)
		}
		if err := writer.member("path", 5); err != nil {
			return nil, err
		}
		if err := writer.string(change.Path); err != nil {
			return nil, fmt.Errorf("changed-files changes[%d].path: %w", index, err)
		}
		if err := writer.member("previous_path", 6); err != nil {
			return nil, err
		}
		if err := writer.nullableString(change.PreviousPath); err != nil {
			return nil, fmt.Errorf("changed-files changes[%d].previous_path: %w", index, err)
		}
		if err := writer.member("source", 7); err != nil {
			return nil, err
		}
		if err := writer.string(change.Source); err != nil {
			return nil, fmt.Errorf("changed-files changes[%d].source: %w", index, err)
		}
		if err := writer.member("status", 8); err != nil {
			return nil, err
		}
		if err := writer.string(change.Status); err != nil {
			return nil, fmt.Errorf("changed-files changes[%d].status: %w", index, err)
		}

		writer.endObject(true)
	}
	writer.endArray(len(changes.Changes) != 0)

	if err := writer.member("schema_version", 2); err != nil {
		return nil, err
	}
	writer.integer(1)
	if err := writer.member("scope", 3); err != nil {
		return nil, err
	}
	writer.beginArray()
	for index, path := range changes.Scope {
		writer.element(index)
		if err := writer.string(path); err != nil {
			return nil, fmt.Errorf("changed-files scope[%d]: %w", index, err)
		}
	}
	writer.endArray(len(changes.Scope) != 0)

	writer.endObject(true)
	writer.buffer.WriteByte('\n')
	return writer.buffer.Bytes(), nil
}

func renderSnapshot(snapshot Snapshot, includeDigest, pretty, asciiOnly bool) ([]byte, error) {
	writer := jsonWriter{pretty: pretty, asciiOnly: asciiOnly}
	writer.beginObject()

	if err := writer.member("baseline", 0); err != nil {
		return nil, err
	}
	if err := writer.string(snapshot.Baseline); err != nil {
		return nil, fmt.Errorf("source snapshot baseline: %w", err)
	}
	if err := writer.member("entries", 1); err != nil {
		return nil, err
	}
	writer.beginArray()
	for index, entry := range snapshot.Entries {
		writer.element(index)
		writer.beginObject()

		if err := writer.member("mode", 0); err != nil {
			return nil, err
		}
		if err := writer.nullableString(entry.Mode); err != nil {
			return nil, fmt.Errorf("source snapshot entries[%d].mode: %w", index, err)
		}
		if err := writer.member("path", 1); err != nil {
			return nil, err
		}
		if err := writer.string(entry.Path); err != nil {
			return nil, fmt.Errorf("source snapshot entries[%d].path: %w", index, err)
		}
		if err := writer.member("sha256", 2); err != nil {
			return nil, err
		}
		if err := writer.nullableString(entry.SHA256); err != nil {
			return nil, fmt.Errorf("source snapshot entries[%d].sha256: %w", index, err)
		}
		if err := writer.member("size_bytes", 3); err != nil {
			return nil, err
		}
		writer.nullableInteger(entry.SizeBytes)
		if err := writer.member("state", 4); err != nil {
			return nil, err
		}
		if err := writer.string(entry.State); err != nil {
			return nil, fmt.Errorf("source snapshot entries[%d].state: %w", index, err)
		}

		writer.endObject(true)
	}
	writer.endArray(len(snapshot.Entries) != 0)

	if err := writer.member("schema_version", 2); err != nil {
		return nil, err
	}
	writer.integer(int64(snapshot.SchemaVersion))
	if includeDigest {
		if err := writer.member("snapshot_sha256", 3); err != nil {
			return nil, err
		}
		if err := writer.string(snapshot.SHA256); err != nil {
			return nil, fmt.Errorf("source snapshot snapshot_sha256: %w", err)
		}
	}

	writer.endObject(true)
	if pretty {
		writer.buffer.WriteByte('\n')
	}
	return writer.buffer.Bytes(), nil
}

// jsonWriter implements only the JSON types needed by the two fixed schemas.
// This avoids encoding/json's mandatory HTML-adjacent escaping while retaining
// Python-compatible lowercase Unicode escapes in the digest payload.
type jsonWriter struct {
	buffer    bytes.Buffer
	pretty    bool
	asciiOnly bool
	depth     int
}

func (writer *jsonWriter) beginObject() {
	writer.buffer.WriteByte('{')
	writer.depth++
}

func (writer *jsonWriter) endObject(nonEmpty bool) {
	writer.depth--
	if writer.pretty && nonEmpty {
		writer.newlineAndIndent()
	}
	writer.buffer.WriteByte('}')
}

func (writer *jsonWriter) beginArray() {
	writer.buffer.WriteByte('[')
	writer.depth++
}

func (writer *jsonWriter) endArray(nonEmpty bool) {
	writer.depth--
	if writer.pretty && nonEmpty {
		writer.newlineAndIndent()
	}
	writer.buffer.WriteByte(']')
}

func (writer *jsonWriter) member(name string, index int) error {
	writer.element(index)
	if err := writer.string(name); err != nil {
		return fmt.Errorf("JSON member name: %w", err)
	}
	writer.buffer.WriteByte(':')
	if writer.pretty {
		writer.buffer.WriteByte(' ')
	}
	return nil
}

func (writer *jsonWriter) element(index int) {
	if index != 0 {
		writer.buffer.WriteByte(',')
	}
	if writer.pretty {
		writer.newlineAndIndent()
	}
}

func (writer *jsonWriter) newlineAndIndent() {
	writer.buffer.WriteByte('\n')
	for count := 0; count < writer.depth*2; count++ {
		writer.buffer.WriteByte(' ')
	}
}

func (writer *jsonWriter) string(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("value is not valid UTF-8")
	}
	writer.buffer.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			writer.buffer.WriteString(`\"`)
		case '\\':
			writer.buffer.WriteString(`\\`)
		case '\b':
			writer.buffer.WriteString(`\b`)
		case '\f':
			writer.buffer.WriteString(`\f`)
		case '\n':
			writer.buffer.WriteString(`\n`)
		case '\r':
			writer.buffer.WriteString(`\r`)
		case '\t':
			writer.buffer.WriteString(`\t`)
		default:
			switch {
			case character < 0x20:
				writer.writeUnicodeEscape(uint16(character))
			case writer.asciiOnly && character >= 0x7f && character <= 0xffff:
				writer.writeUnicodeEscape(uint16(character))
			case writer.asciiOnly && character > 0xffff:
				value := character - 0x10000
				writer.writeUnicodeEscape(uint16(0xd800 + value>>10))
				writer.writeUnicodeEscape(uint16(0xdc00 + value&0x3ff))
			default:
				writer.buffer.WriteRune(character)
			}
		}
	}
	writer.buffer.WriteByte('"')
	return nil
}

func (writer *jsonWriter) writeUnicodeEscape(value uint16) {
	const hexadecimal = "0123456789abcdef"
	writer.buffer.WriteString(`\u`)
	writer.buffer.WriteByte(hexadecimal[value>>12&0xf])
	writer.buffer.WriteByte(hexadecimal[value>>8&0xf])
	writer.buffer.WriteByte(hexadecimal[value>>4&0xf])
	writer.buffer.WriteByte(hexadecimal[value&0xf])
}

func (writer *jsonWriter) nullableString(value *string) error {
	if value == nil {
		writer.buffer.WriteString("null")
		return nil
	}
	return writer.string(*value)
}

func (writer *jsonWriter) integer(value int64) {
	writer.buffer.WriteString(strconv.FormatInt(value, 10))
}

func (writer *jsonWriter) nullableInteger(value *int64) {
	if value == nil {
		writer.buffer.WriteString("null")
		return
	}
	writer.integer(*value)
}

func (writer *jsonWriter) boolean(value bool) {
	writer.buffer.WriteString(strconv.FormatBool(value))
}
