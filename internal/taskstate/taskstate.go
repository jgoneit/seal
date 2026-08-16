// Package taskstate implements Task snapshot creation and exact stored lookup.
package taskstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Document is an opaque, syntactically valid stored Task JSON object. Task show
// does not assign schema or Acceptance meaning to the object's fields.
type Document struct {
	values                  map[string]any
	taskID                  string
	hasUnencodableSurrogate bool
	surrogateEscapeMarker   string
}

type pythonConstant uint8

const (
	pythonNaN pythonConstant = iota + 1
	pythonPositiveInfinity
	pythonNegativeInfinity
)

type constantMarkers struct {
	nan              string
	positiveInfinity string
	negativeInfinity string
}

type jsonDecodeFailure struct {
	cause            error
	numericScanLimit int
}

func (e *jsonDecodeFailure) Error() string {
	return e.cause.Error()
}

func (e *jsonDecodeFailure) Unwrap() error {
	return e.cause
}

// ErrorKind identifies the stable command error class used by the CLI.
type ErrorKind uint8

const (
	// InvalidInput identifies invalid Task creation inputs and destinations,
	// invalid identities, missing snapshots, and invalid stored JSON. The CLI
	// maps it to exit code 2.
	InvalidInput ErrorKind = iota + 1
	// Repository identifies failures to discover the current Git repository.
	// The CLI maps it to exit code 3.
	Repository
	// EncodingFailure identifies frozen-Reference text encoding failures that
	// escape its handled invalid-input boundary. The CLI maps it to exit code 1.
	EncodingFailure
	// NumericFailure identifies CPython JSON integer conversion failures that
	// escape its handled invalid-input boundary. The CLI maps it to exit code 1.
	NumericFailure
	// NestingLimitFailure identifies the explicitly approved standard JSON
	// decoder nesting-limit divergence. The CLI maps it to exit code 1.
	NestingLimitFailure
)

// Error is a classified Task creation or lookup failure.
type Error struct {
	kind    ErrorKind
	message string
	cause   error
}

// Error returns the public error text.
func (e *Error) Error() string {
	return e.message
}

// Unwrap exposes the underlying operating-system or JSON error, when present.
func (e *Error) Unwrap() error {
	return e.cause
}

// Kind returns the stable command error class.
func (e *Error) Kind() ErrorKind {
	return e.kind
}

// KindOf returns the stable command error class carried by err.
func KindOf(err error) (ErrorKind, bool) {
	var taskError *Error
	if !errors.As(err, &taskError) {
		return 0, false
	}
	return taskError.kind, true
}

// Show reads one exact stored Task snapshot from the Git repository containing
// cwd. It validates only the requested identity and the stored JSON object
// shape; full Task validation belongs to later Acceptance-authority boundaries.
func Show(cwd, taskID string) (Document, error) {
	if err := validateID(taskID); err != nil {
		return Document{}, err
	}

	repository, err := findRepositoryRoot(cwd)
	if err != nil {
		return Document{}, err
	}

	path := filepath.Join(repository, ".seal", "tasks", taskID+".json")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Document{}, invalidInput(fmt.Sprintf("Task '%s' does not exist.", taskID), err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return Document{}, invalidInput(
			fmt.Sprintf("Could not read Task snapshot '%s': %s.", taskID, path),
			err,
		)
	}
	if !utf8.Valid(contents) {
		return Document{}, encodingFailure(
			fmt.Sprintf("Task snapshot '%s' is not valid UTF-8.", taskID),
			nil,
		)
	}
	normalized, markers := replacePythonConstants(contents)
	value, err := decodeJSONValue(normalized)
	if err != nil {
		if containsOversizedPythonInteger(normalized[:numericScanLimit(normalized, err)]) {
			return Document{}, oversizedPythonInteger(taskID)
		}
		if isStandardJSONDepthLimit(err) {
			return Document{}, nestingLimitFailure(
				fmt.Sprintf("Task snapshot '%s' exceeds the supported JSON nesting depth.", taskID),
				err,
			)
		}
		return Document{}, invalidJSON(taskID, err)
	}
	if containsOversizedPythonInteger(contents) {
		return Document{}, oversizedPythonInteger(taskID)
	}
	if _, ok := value.(map[string]any); !ok {
		return Document{}, invalidInput(
			fmt.Sprintf("Task snapshot '%s' must be a JSON object.", taskID),
			nil,
		)
	}

	surrogateEscapeMarker := chooseSurrogateEscapeMarker(normalized)
	rewritten, hasRewrittenSurrogate := rewriteSurrogateEscapes(
		normalized,
		surrogateEscapeMarker,
	)
	if hasRewrittenSurrogate {
		value, err = decodeJSONValue(rewritten)
		if err != nil {
			if isStandardJSONDepthLimit(err) {
				return Document{}, nestingLimitFailure(
					fmt.Sprintf("Task snapshot '%s' exceeds the supported JSON nesting depth.", taskID),
					err,
				)
			}
			return Document{}, invalidJSON(taskID, err)
		}
	} else {
		surrogateEscapeMarker = ""
	}
	document, ok := restorePythonConstants(value, markers).(map[string]any)
	if !ok {
		return Document{}, invalidInput(
			fmt.Sprintf("Task snapshot '%s' must be a JSON object.", taskID),
			nil,
		)
	}
	return Document{
		values:                  document,
		taskID:                  taskID,
		hasUnencodableSurrogate: containsUnencodableSurrogate(document, surrogateEscapeMarker),
		surrogateEscapeMarker:   surrogateEscapeMarker,
	}, nil
}

// Render returns the frozen Python Reference's sorted, two-space-indented JSON
// representation of a stored Task object. It intentionally normalizes numbers
// as Python json.load/json.dumps do, without interpreting any Task field.
// The returned bytes do not include the CLI's final newline. They can contain
// raw bytes 0x80-0xff when the stored JSON uses Python's DC80-DCFF
// surrogateescape range, so callers must write the result as bytes.
func Render(document Document) ([]byte, error) {
	if document.hasUnencodableSurrogate {
		return nil, encodingFailure(
			fmt.Sprintf(
				"Task snapshot '%s' cannot be rendered as UTF-8 because it contains an unpaired UTF-16 surrogate escape.",
				document.taskID,
			),
			nil,
		)
	}
	var output bytes.Buffer
	if err := renderValue(&output, document.values, 0, document.surrogateEscapeMarker); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeJSONValue(contents []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		limit := len(contents)
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) && syntaxError.Offset >= 0 && syntaxError.Offset < int64(limit) {
			limit = int(syntaxError.Offset)
		}
		return nil, &jsonDecodeFailure{cause: err, numericScanLimit: limit}
	}

	firstValueEnd := int(decoder.InputOffset())
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, &jsonDecodeFailure{cause: err, numericScanLimit: firstValueEnd}
	}
	return value, nil
}

func numericScanLimit(contents []byte, err error) int {
	var failure *jsonDecodeFailure
	if errors.As(err, &failure) && failure.numericScanLimit >= 0 && failure.numericScanLimit <= len(contents) {
		return failure.numericScanLimit
	}
	return 0
}

func isStandardJSONDepthLimit(err error) bool {
	var syntaxError *json.SyntaxError
	return errors.As(err, &syntaxError) && strings.Contains(syntaxError.Error(), "exceeded max depth")
}

func containsOversizedPythonInteger(contents []byte) bool {
	const maximumDigits = 4300
	inString := false
	for index := 0; index < len(contents); {
		character := contents[index]
		if inString {
			index++
			if character == '\\' && index < len(contents) {
				index++
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			index++
			continue
		}

		numberStart := index
		if character == '-' {
			index++
			if index >= len(contents) || contents[index] < '0' || contents[index] > '9' {
				continue
			}
		} else if character < '0' || character > '9' {
			index++
			continue
		}

		digitsStart := index
		for index < len(contents) && contents[index] >= '0' && contents[index] <= '9' {
			index++
		}
		digitCount := index - digitsStart
		isInteger := true
		if index+1 < len(contents) && contents[index] == '.' &&
			contents[index+1] >= '0' && contents[index+1] <= '9' {
			isInteger = false
			index++
			for index < len(contents) && contents[index] >= '0' && contents[index] <= '9' {
				index++
			}
		}
		exponentStart := index
		if index < len(contents) && (contents[index] == 'e' || contents[index] == 'E') {
			index++
			if index < len(contents) && (contents[index] == '+' || contents[index] == '-') {
				index++
			}
			if index >= len(contents) || contents[index] < '0' || contents[index] > '9' {
				index = exponentStart
			} else {
				isInteger = false
				for index < len(contents) && contents[index] >= '0' && contents[index] <= '9' {
					index++
				}
			}
		}
		if isInteger && digitCount > maximumDigits {
			return true
		}
		if index == numberStart {
			index++
		}
	}
	return false
}

func validateID(taskID string) error {
	if taskID == "" {
		return invalidInput("Task id must be a non-empty string.", nil)
	}
	if !isASCIIAlphanumeric(taskID[0]) {
		return invalidInput(
			"Task id must begin with an alphanumeric character and contain only letters, numbers, underscores, or hyphens.",
			nil,
		)
	}
	for index := 1; index < len(taskID); index++ {
		character := taskID[index]
		if !isASCIIAlphanumeric(character) && character != '_' && character != '-' {
			return invalidInput(
				"Task id must contain only letters, numbers, underscores, or hyphens.",
				nil,
			)
		}
	}
	return nil
}

func isASCIIAlphanumeric(character byte) bool {
	return character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9'
}

func findRepositoryRoot(cwd string) (string, error) {
	if cwd == "" {
		cwd = "."
	}

	command := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	stdout, err := command.Output()
	if err != nil {
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return "", repositoryError("Git is required to create or show a task.", err)
		}
		return "", repositoryError("Task commands must run inside a Git repository.", err)
	}

	root := strings.TrimSpace(string(stdout))
	if root == "" {
		return "", repositoryError("Task commands must run inside a Git repository.", nil)
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err == nil {
		return resolved, nil
	}
	absolute, absoluteError := filepath.Abs(root)
	if absoluteError == nil {
		return filepath.Clean(absolute), nil
	}
	return filepath.Clean(root), nil
}

func replacePythonConstants(contents []byte) ([]byte, constantMarkers) {
	markers := chooseConstantMarkers(contents)
	var output bytes.Buffer
	output.Grow(len(contents))
	inString := false

	for index := 0; index < len(contents); {
		character := contents[index]
		if inString {
			output.WriteByte(character)
			index++
			if character == '\\' && index < len(contents) {
				output.WriteByte(contents[index])
				index++
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}

		if character == '"' {
			inString = true
			output.WriteByte(character)
			index++
			continue
		}

		switch {
		case bytes.HasPrefix(contents[index:], []byte("-Infinity")):
			writeMarker(&output, markers.negativeInfinity)
			index += len("-Infinity")
		case bytes.HasPrefix(contents[index:], []byte("Infinity")):
			writeMarker(&output, markers.positiveInfinity)
			index += len("Infinity")
		case bytes.HasPrefix(contents[index:], []byte("NaN")):
			writeMarker(&output, markers.nan)
			index += len("NaN")
		default:
			output.WriteByte(character)
			index++
		}
	}
	return output.Bytes(), markers
}

func chooseSurrogateEscapeMarker(contents []byte) string {
	stringsInDocument := decodedJSONStrings(contents)
	marker := "\ue000"
	for stringsContain(stringsInDocument, marker) {
		marker += "\ue000"
	}
	return marker
}

func decodedJSONStrings(contents []byte) []string {
	var result []string
	for index := 0; index < len(contents); {
		if contents[index] != '"' {
			index++
			continue
		}
		start := index
		index++
		for index < len(contents) {
			switch contents[index] {
			case '\\':
				index += 2
			case '"':
				index++
				var decoded string
				if json.Unmarshal(contents[start:index], &decoded) == nil {
					result = append(result, decoded)
				}
				goto nextString
			default:
				index++
			}
		}
	nextString:
	}
	return result
}

func stringsContain(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func rewriteSurrogateEscapes(contents []byte, marker string) ([]byte, bool) {
	var output bytes.Buffer
	output.Grow(len(contents))
	hasRewrittenSurrogate := false
	inString := false
	for index := 0; index < len(contents); {
		character := contents[index]
		if !inString {
			if character == '"' {
				inString = true
			}
			output.WriteByte(character)
			index++
			continue
		}

		if character == '"' {
			inString = false
			output.WriteByte(character)
			index++
			continue
		}
		if character != '\\' {
			output.WriteByte(character)
			index++
			continue
		}
		if index+1 >= len(contents) {
			output.WriteByte(character)
			index++
			continue
		}
		if contents[index+1] != 'u' {
			output.Write(contents[index : index+2])
			index += 2
			continue
		}

		codeUnit, ok := decodeUTF16Escape(contents[index:])
		if !ok {
			output.Write(contents[index : index+2])
			index += 2
			continue
		}
		if codeUnit < 0xd800 || codeUnit > 0xdfff {
			output.Write(contents[index : index+6])
			index += 6
			continue
		}

		if codeUnit >= 0xdc80 && codeUnit <= 0xdcff {
			writeSurrogateEscapeMarker(&output, marker, byte(codeUnit-0xdc00))
			hasRewrittenSurrogate = true
			index += 6
			continue
		}

		if codeUnit >= 0xdc00 {
			writeUnencodableSurrogateMarker(&output, marker, codeUnit)
			hasRewrittenSurrogate = true
			index += 6
			continue
		}

		lowSurrogate, paired := decodeUTF16Escape(contents[index+6:])
		if paired && lowSurrogate >= 0xdc00 && lowSurrogate <= 0xdfff {
			output.Write(contents[index : index+12])
			index += 12
			continue
		}

		writeUnencodableSurrogateMarker(&output, marker, codeUnit)
		hasRewrittenSurrogate = true
		index += 6
	}
	return output.Bytes(), hasRewrittenSurrogate
}

func writeSurrogateEscapeMarker(output *bytes.Buffer, marker string, value byte) {
	const hexadecimal = "0123456789abcdef"
	output.WriteString(marker)
	output.WriteByte('r')
	output.WriteByte(hexadecimal[value>>4])
	output.WriteByte(hexadecimal[value&0x0f])
}

func writeUnencodableSurrogateMarker(output *bytes.Buffer, marker string, value uint16) {
	const hexadecimal = "0123456789abcdef"
	output.WriteString(marker)
	output.WriteByte('u')
	for shift := 12; shift >= 0; shift -= 4 {
		output.WriteByte(hexadecimal[byte(value>>shift)&0x0f])
	}
}

func containsUnencodableSurrogate(value any, marker string) bool {
	if marker == "" {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, marker+"u")
	case map[string]any:
		for key, entry := range typed {
			if strings.Contains(key, marker+"u") || containsUnencodableSurrogate(entry, marker) {
				return true
			}
		}
	case []any:
		for _, entry := range typed {
			if containsUnencodableSurrogate(entry, marker) {
				return true
			}
		}
	}
	return false
}

func decodeUTF16Escape(contents []byte) (uint16, bool) {
	if len(contents) < 6 || contents[0] != '\\' || contents[1] != 'u' {
		return 0, false
	}
	var value uint16
	for _, character := range contents[2:6] {
		digit, ok := hexadecimalDigit(character)
		if !ok {
			return 0, false
		}
		value = value*16 + uint16(digit)
	}
	return value, true
}

func hexadecimalDigit(character byte) (byte, bool) {
	switch {
	case character >= '0' && character <= '9':
		return character - '0', true
	case character >= 'a' && character <= 'f':
		return character - 'a' + 10, true
	case character >= 'A' && character <= 'F':
		return character - 'A' + 10, true
	default:
		return 0, false
	}
}

func chooseConstantMarkers(contents []byte) constantMarkers {
	for exponent := 1_000_000; ; exponent += 3 {
		markers := constantMarkers{
			nan:              fmt.Sprintf("2e%d", exponent),
			positiveInfinity: fmt.Sprintf("3e%d", exponent+1),
			negativeInfinity: fmt.Sprintf("-4e%d", exponent+2),
		}
		if !bytes.Contains(contents, []byte(markers.nan)) &&
			!bytes.Contains(contents, []byte(markers.positiveInfinity)) &&
			!bytes.Contains(contents, []byte(markers.negativeInfinity)) {
			return markers
		}
	}
}

func writeMarker(output *bytes.Buffer, marker string) {
	output.WriteByte(' ')
	output.WriteString(marker)
	output.WriteByte(' ')
}

func restorePythonConstants(value any, markers constantMarkers) any {
	switch typed := value.(type) {
	case json.Number:
		switch string(typed) {
		case markers.nan:
			return pythonNaN
		case markers.positiveInfinity:
			return pythonPositiveInfinity
		case markers.negativeInfinity:
			return pythonNegativeInfinity
		default:
			return typed
		}
	case map[string]any:
		for key, entry := range typed {
			typed[key] = restorePythonConstants(entry, markers)
		}
		return typed
	case []any:
		for index, entry := range typed {
			typed[index] = restorePythonConstants(entry, markers)
		}
		return typed
	default:
		return value
	}
}

func renderValue(output *bytes.Buffer, value any, depth int, surrogateEscapeMarker string) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		renderString(output, typed, surrogateEscapeMarker)
	case json.Number:
		number, err := renderNumber(typed)
		if err != nil {
			return err
		}
		output.WriteString(number)
	case pythonConstant:
		switch typed {
		case pythonNaN:
			output.WriteString("NaN")
		case pythonPositiveInfinity:
			output.WriteString("Infinity")
		case pythonNegativeInfinity:
			output.WriteString("-Infinity")
		default:
			return fmt.Errorf("taskstate: unknown Python numeric constant %d", typed)
		}
	case map[string]any:
		return renderObject(output, typed, depth, surrogateEscapeMarker)
	case []any:
		return renderArray(output, typed, depth, surrogateEscapeMarker)
	default:
		return fmt.Errorf("taskstate: unsupported JSON value type %T", value)
	}
	return nil
}

func renderObject(
	output *bytes.Buffer,
	value map[string]any,
	depth int,
	surrogateEscapeMarker string,
) error {
	if len(value) == 0 {
		output.WriteString("{}")
		return nil
	}

	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		return comparePythonStrings(keys[left], keys[right], surrogateEscapeMarker) < 0
	})

	output.WriteString("{\n")
	for index, key := range keys {
		writeIndent(output, depth+1)
		renderString(output, key, surrogateEscapeMarker)
		output.WriteString(": ")
		if err := renderValue(output, value[key], depth+1, surrogateEscapeMarker); err != nil {
			return err
		}
		if index+1 < len(keys) {
			output.WriteByte(',')
		}
		output.WriteByte('\n')
	}
	writeIndent(output, depth)
	output.WriteByte('}')
	return nil
}

func renderArray(
	output *bytes.Buffer,
	value []any,
	depth int,
	surrogateEscapeMarker string,
) error {
	if len(value) == 0 {
		output.WriteString("[]")
		return nil
	}

	output.WriteString("[\n")
	for index, entry := range value {
		writeIndent(output, depth+1)
		if err := renderValue(output, entry, depth+1, surrogateEscapeMarker); err != nil {
			return err
		}
		if index+1 < len(value) {
			output.WriteByte(',')
		}
		output.WriteByte('\n')
	}
	writeIndent(output, depth)
	output.WriteByte(']')
	return nil
}

func renderString(output *bytes.Buffer, value, surrogateEscapeMarker string) {
	const hexadecimal = "0123456789abcdef"
	output.WriteByte('"')
	for index := 0; index < len(value); {
		if rawByte, size, ok := decodeSurrogateEscapeMarker(
			value[index:],
			surrogateEscapeMarker,
		); ok {
			output.WriteByte(rawByte)
			index += size
			continue
		}

		character, size := utf8.DecodeRuneInString(value[index:])
		index += size
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '\b':
			output.WriteString("\\b")
		case '\f':
			output.WriteString("\\f")
		case '\n':
			output.WriteString("\\n")
		case '\r':
			output.WriteString("\\r")
		case '\t':
			output.WriteString("\\t")
		default:
			if character < 0x20 {
				output.WriteString("\\u00")
				output.WriteByte(hexadecimal[byte(character)>>4])
				output.WriteByte(hexadecimal[byte(character)&0x0f])
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
}

func decodeSurrogateEscapeMarker(value, marker string) (byte, int, bool) {
	if marker == "" || !strings.HasPrefix(value, marker) {
		return 0, 0, false
	}
	markerEnd := len(marker)
	if len(value) < markerEnd+3 || value[markerEnd] != 'r' {
		return 0, 0, false
	}
	high, highOK := hexadecimalDigit(value[markerEnd+1])
	low, lowOK := hexadecimalDigit(value[markerEnd+2])
	if !highOK || !lowOK {
		return 0, 0, false
	}
	return high<<4 | low, markerEnd + 3, true
}

func comparePythonStrings(left, right, surrogateEscapeMarker string) int {
	leftRunes := pythonStringRunes(left, surrogateEscapeMarker)
	rightRunes := pythonStringRunes(right, surrogateEscapeMarker)
	limit := min(len(leftRunes), len(rightRunes))
	for index := range limit {
		if leftRunes[index] < rightRunes[index] {
			return -1
		}
		if leftRunes[index] > rightRunes[index] {
			return 1
		}
	}
	if len(leftRunes) < len(rightRunes) {
		return -1
	}
	if len(leftRunes) > len(rightRunes) {
		return 1
	}
	return 0
}

func pythonStringRunes(value, surrogateEscapeMarker string) []rune {
	result := make([]rune, 0, utf8.RuneCountInString(value))
	for index := 0; index < len(value); {
		if rawByte, size, ok := decodeSurrogateEscapeMarker(
			value[index:],
			surrogateEscapeMarker,
		); ok {
			result = append(result, rune(0xdc00)+rune(rawByte))
			index += size
			continue
		}
		character, size := utf8.DecodeRuneInString(value[index:])
		result = append(result, character)
		index += size
	}
	return result
}

func renderNumber(number json.Number) (string, error) {
	lexeme := string(number)
	if !strings.ContainsAny(lexeme, ".eE") {
		integer := new(big.Int)
		if _, ok := integer.SetString(lexeme, 10); !ok {
			return "", fmt.Errorf("taskstate: invalid JSON integer %q", lexeme)
		}
		return integer.String(), nil
	}

	value, err := strconv.ParseFloat(lexeme, 64)
	if err != nil {
		var numberError *strconv.NumError
		if !errors.As(err, &numberError) || numberError.Err != strconv.ErrRange {
			return "", fmt.Errorf("taskstate: invalid JSON float %q: %w", lexeme, err)
		}
	}
	return renderFloat(value), nil
}

func renderFloat(value float64) string {
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	if math.IsNaN(value) {
		return "NaN"
	}
	if value == 0 {
		if math.Signbit(value) {
			return "-0.0"
		}
		return "0.0"
	}

	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponentIndex := strings.LastIndexByte(scientific, 'e')
	exponent, err := strconv.Atoi(scientific[exponentIndex+1:])
	if err == nil && exponent >= -4 && exponent < 16 {
		fixed := strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.ContainsRune(fixed, '.') {
			fixed += ".0"
		}
		return fixed
	}
	return scientific
}

func writeIndent(output *bytes.Buffer, depth int) {
	for range depth * 2 {
		output.WriteByte(' ')
	}
}

func invalidJSON(taskID string, cause error) error {
	return invalidInput(
		fmt.Sprintf("Task snapshot '%s' is not valid JSON: %s.", taskID, cause),
		cause,
	)
}

func invalidInput(message string, cause error) error {
	return &Error{kind: InvalidInput, message: message, cause: cause}
}

func repositoryError(message string, cause error) error {
	return &Error{kind: Repository, message: message, cause: cause}
}

func encodingFailure(message string, cause error) error {
	return &Error{kind: EncodingFailure, message: message, cause: cause}
}

func numericFailure(message string, cause error) error {
	return &Error{kind: NumericFailure, message: message, cause: cause}
}

func oversizedPythonInteger(taskID string) error {
	return numericFailure(
		fmt.Sprintf(
			"Task snapshot '%s' contains a JSON integer exceeding CPython's limit of 4300 digits.",
			taskID,
		),
		nil,
	)
}

func nestingLimitFailure(message string, cause error) error {
	return &Error{kind: NestingLimitFailure, message: message, cause: cause}
}
