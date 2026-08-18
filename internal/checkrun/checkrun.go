// Package checkrun executes the saved checks for one Acceptance Task in order.
// It records process outcomes and raw logs but does not collect source state,
// evaluate Scope, or publish an Evidence Run.
package checkrun

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultTimeoutSeconds is the effective timeout when a saved check does not
	// provide timeout_seconds.
	DefaultTimeoutSeconds int64 = 300

	processTerminateGrace = 200 * time.Millisecond
)

var maxTimerSeconds = big.NewInt(math.MaxInt64 / int64(time.Second))

// Definition is one already-resolved saved Task check. TimeoutSeconds is nil
// when the reference default applies. RunRooted clones all mutable fields before use.
type Definition struct {
	Name           string
	Argv           []string
	Required       bool
	TimeoutSeconds *big.Int
}

// Result records one attempted check; EffectiveTimeout remains an unquoted,
// arbitrary-precision JSON integer.
type Result struct {
	Argv             []string `json:"argv"`
	CWD              string   `json:"cwd"`
	DurationSeconds  float64  `json:"duration_seconds"`
	EffectiveTimeout *big.Int `json:"effective_timeout"`
	ExitCode         *int64   `json:"exit_code"`
	FinishedAt       string   `json:"finished_at"`
	Name             string   `json:"name"`
	Passed           bool     `json:"passed"`
	Required         bool     `json:"required"`
	StartedAt        string   `json:"started_at"`
	StderrPath       string   `json:"stderr_path"`
	StdoutPath       string   `json:"stdout_path"`
	TimedOut         bool     `json:"timed_out"`
}

// DefinitionError reports a saved check that cannot be executed safely.
type DefinitionError struct{ message string }

func (e *DefinitionError) Error() string { return e.message }

// InfrastructureError reports a failure to prepare logs, wait for a managed
// process, or clean up its process tree. Process launch failures are instead
// recorded as ordinary failed check results.
type InfrastructureError struct {
	message string
	cause   error
}

func (e *InfrastructureError) Error() string { return e.message }
func (e *InfrastructureError) Unwrap() error { return e.cause }

// RunRooted executes checks while creating every log relative to an already
// opened Evidence root. The caller retains ownership of evidenceRoot. This is
// the publication-safe entry point: it never reconstructs a mutable absolute
// path for logs.
func RunRooted(checks []Definition, repositoryRoot string, evidenceRoot *os.Root) ([]Result, error) {
	workingDirectory, err := resolveDirectory(repositoryRoot, "check working directory")
	if err != nil {
		return nil, err
	}
	if evidenceRoot == nil {
		return nil, infrastructure("Could not use check Evidence directory.", errors.New("nil Evidence root"))
	}
	info, err := evidenceRoot.Stat(".")
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("Evidence root is not a directory")
		}
		return nil, infrastructure("Could not use check Evidence directory.", err)
	}
	return runRooted(checks, workingDirectory, evidenceRoot)
}

func runRooted(checks []Definition, workingDirectory string, evidenceRoot *os.Root) ([]Result, error) {
	if err := evidenceRoot.Mkdir("checks", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, infrastructure("Could not create check log directory.", err)
	}
	info, err := evidenceRoot.Lstat("checks")
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err == nil {
			err = errors.New("check log path is not a real directory")
		}
		return nil, infrastructure("Could not use check log directory.", err)
	}
	if err := evidenceRoot.Chmod("checks", 0o700); err != nil {
		return nil, infrastructure("Could not make check log directory private.", err)
	}
	outputDirectory, err := evidenceRoot.OpenRoot("checks")
	if err != nil {
		return nil, infrastructure("Could not open check log directory.", err)
	}
	defer outputDirectory.Close()

	results := make([]Result, 0, len(checks))
	for index, supplied := range checks {
		definition, timeout, err := validatedDefinition(supplied, index)
		if err != nil {
			return nil, err
		}
		result, err := runOne(
			definition,
			timeout,
			index,
			workingDirectory,
			outputDirectory,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func runOne(
	definition Definition,
	timeout *big.Int,
	index int,
	workingDirectory string,
	outputDirectory *os.Root,
) (Result, error) {
	startedAt := utcTimestamp(time.Now())
	started := time.Now()
	stem := outputStem(index, definition.Name)
	stdoutName := stem + ".stdout"
	stderrName := stem + ".stderr"

	stdout, err := openPrivateLog(outputDirectory, stdoutName)
	if err != nil {
		return Result{}, infrastructure("Could not open check stdout log.", err)
	}
	stderr, err := openPrivateLog(outputDirectory, stderrName)
	if err != nil {
		_ = stdout.Close()
		return Result{}, infrastructure("Could not open check stderr log.", err)
	}

	var exitCode *int64
	timedOut := false
	process, launchError := startProcess(
		definition.Argv,
		workingDirectory,
		os.Stdin,
		stdout,
		stderr,
	)
	if launchError != nil {
		if _, err := fmt.Fprintf(stderr, "Could not start check: %v\n", launchError); err != nil {
			_ = stdout.Close()
			_ = stderr.Close()
			return Result{}, infrastructure("Could not write check launch failure.", err)
		}
	} else {
		outcomeChannel := make(chan processOutcome, 1)
		go func() {
			outcomeChannel <- process.wait()
		}()

		outcome, timeoutReached, err := waitForProcess(process, outcomeChannel, timeout)
		timedOut = timeoutReached
		if err == nil && !timeoutReached {
			err = process.cleanupAfterExit()
		}
		closeError := process.close()
		if err == nil {
			err = closeError
		}
		if err != nil {
			_ = stdout.Close()
			_ = stderr.Close()
			return Result{}, infrastructure("Could not finish managed check process.", err)
		}
		if outcome.err != nil {
			_ = stdout.Close()
			_ = stderr.Close()
			return Result{}, infrastructure("Could not wait for check process.", outcome.err)
		}
		code := outcome.exitCode
		exitCode = &code
	}

	if err := stdout.Close(); err != nil {
		_ = stderr.Close()
		return Result{}, infrastructure("Could not close check stdout log.", err)
	}
	if err := stderr.Close(); err != nil {
		return Result{}, infrastructure("Could not close check stderr log.", err)
	}

	duration := time.Since(started).Seconds()
	if duration < 0 {
		duration = 0
	}
	passed := !timedOut && exitCode != nil && *exitCode == 0
	return Result{
		Argv:             append([]string(nil), definition.Argv...),
		CWD:              workingDirectory,
		DurationSeconds:  duration,
		EffectiveTimeout: new(big.Int).Set(timeout),
		ExitCode:         exitCode,
		FinishedAt:       utcTimestamp(time.Now()),
		Name:             definition.Name,
		Passed:           passed,
		Required:         definition.Required,
		StartedAt:        startedAt,
		StderrPath:       filepath.ToSlash(filepath.Join("checks", stderrName)),
		StdoutPath:       filepath.ToSlash(filepath.Join("checks", stdoutName)),
		TimedOut:         timedOut,
	}, nil
}

func waitForProcess(
	process managedProcess,
	outcomeChannel <-chan processOutcome,
	timeout *big.Int,
) (processOutcome, bool, error) {
	remaining := new(big.Int).Set(timeout)
	for {
		chunk := new(big.Int).Set(remaining)
		if chunk.Cmp(maxTimerSeconds) > 0 {
			chunk.Set(maxTimerSeconds)
		}
		timer := time.NewTimer(time.Duration(chunk.Int64()) * time.Second)
		select {
		case outcome := <-outcomeChannel:
			stopTimer(timer)
			return outcome, false, nil
		case <-timer.C:
			remaining.Sub(remaining, chunk)
			if remaining.Sign() > 0 {
				continue
			}
			// Give an outcome made ready at the deadline precedence over timeout.
			select {
			case outcome := <-outcomeChannel:
				return outcome, false, nil
			default:
			}
			outcome, err := process.terminate(outcomeChannel, processTerminateGrace)
			return outcome, true, err
		}
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resolveDirectory(path, label string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", infrastructure("Could not resolve "+label+".", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", infrastructure("Could not resolve "+label+".", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", resolved)
		}
		return "", infrastructure("Could not use "+label+".", err)
	}
	return resolved, nil
}

func validatedDefinition(value Definition, index int) (Definition, *big.Int, error) {
	if value.Name == "" {
		return Definition{}, nil, &DefinitionError{message: fmt.Sprintf("checks[%d].name must be a non-empty string.", index)}
	}
	if len(value.Argv) == 0 {
		return Definition{}, nil, &DefinitionError{message: fmt.Sprintf("checks[%d].argv must be a non-empty array.", index)}
	}
	argv := append([]string(nil), value.Argv...)
	for position, argument := range argv {
		if argument == "" {
			return Definition{}, nil, &DefinitionError{message: fmt.Sprintf(
				"checks[%d].argv[%d] must be a non-empty string.",
				index,
				position,
			)}
		}
	}
	timeout := big.NewInt(DefaultTimeoutSeconds)
	if value.TimeoutSeconds != nil {
		timeout.Set(value.TimeoutSeconds)
	}
	if timeout.Sign() <= 0 {
		return Definition{}, nil, &DefinitionError{message: fmt.Sprintf(
			"checks[%d].timeout_seconds must be a positive integer.",
			index,
		)}
	}
	return Definition{
		Name:           value.Name,
		Argv:           argv,
		Required:       value.Required,
		TimeoutSeconds: new(big.Int).Set(timeout),
	}, timeout, nil
}

func outputStem(index int, name string) string {
	var slug strings.Builder
	underscorePending := false
	for _, character := range name {
		if asciiFilenameCharacter(character) {
			if underscorePending && slug.Len() > 0 {
				slug.WriteByte('_')
			}
			underscorePending = false
			slug.WriteRune(character)
		} else {
			underscorePending = true
		}
	}
	normalized := strings.Trim(slug.String(), "._-")
	if normalized == "" {
		normalized = "check"
	}
	if len(normalized) > 48 {
		normalized = normalized[:48]
	}
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%03d-%s-%s", index, normalized, hex.EncodeToString(digest[:])[:12])
}

func asciiFilenameCharacter(character rune) bool {
	return character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9' ||
		character == '_' || character == '.' || character == '-'
}

func openPrivateLog(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func utcTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func infrastructure(message string, cause error) error {
	return &InfrastructureError{message: message + " " + cause.Error(), cause: cause}
}

type processOutcome struct {
	exitCode int64
	err      error
}

type managedProcess interface {
	wait() processOutcome
	terminate(<-chan processOutcome, time.Duration) (processOutcome, error)
	cleanupAfterExit() error
	close() error
}
