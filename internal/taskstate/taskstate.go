// Package taskstate implements Task snapshot creation and exact stored lookup.
package taskstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/jgoneit/seal/internal/gitroot"
	"github.com/jgoneit/seal/internal/pyjson"
)

// Document is an opaque, syntactically valid stored Task JSON object. Task show
// does not assign schema or Acceptance meaning to the object's fields.
type Document struct {
	values map[string]any
	taskID string
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
	value, err := pyjson.Decode(contents)
	if err != nil {
		var limit *pyjson.IntegerLimitError
		if errors.As(err, &limit) {
			return Document{}, oversizedPythonInteger(taskID)
		}
		if pyjson.IsDepthLimit(err) {
			return Document{}, nestingLimitFailure(
				fmt.Sprintf("Task snapshot '%s' exceeds the supported JSON nesting depth.", taskID),
				err,
			)
		}
		return Document{}, invalidJSON(taskID, err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		return Document{}, invalidInput(
			fmt.Sprintf("Task snapshot '%s' must be a JSON object.", taskID),
			nil,
		)
	}
	return Document{values: document, taskID: taskID}, nil
}

// Render returns the frozen Python Reference's sorted, two-space-indented JSON
// representation of a stored Task object. It intentionally normalizes numbers
// as Python json.load/json.dumps do, without interpreting any Task field.
// The returned bytes do not include the CLI's final newline. They can contain
// raw bytes 0x80-0xff when the stored JSON uses Python's DC80-DCFF
// surrogateescape range, so callers must write the result as bytes.
func Render(document Document) ([]byte, error) {
	encoded, err := pyjson.Encode(document.values, pyjson.Options{Indent: true, RawSurrogateBytes: true})
	if err != nil {
		return nil, encodingFailure(
			fmt.Sprintf(
				"Task snapshot '%s' cannot be rendered as UTF-8 because it contains an unpaired UTF-16 surrogate escape.",
				document.taskID,
			),
			err,
		)
	}
	return encoded, nil
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
	root, err := gitroot.Find(cwd)
	if errors.Is(err, gitroot.ErrGitUnavailable) {
		return "", repositoryError("Git is required to create or show a task.", err)
	}
	if err != nil {
		return "", repositoryError("Task commands must run inside a Git repository.", err)
	}
	return root, nil
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
