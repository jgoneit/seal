// Package checkrun executes the saved checks for one Acceptance Task in order.
// It records process outcomes and raw logs but does not collect source state,
// evaluate Scope, or publish an Evidence Run.
package checkrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTimeoutSeconds is the effective timeout when a saved check does not
	// provide timeout_seconds.
	DefaultTimeoutSeconds int64 = 300
	// MaxTimeoutSeconds is the largest timeout accepted by the Basic runner.
	MaxTimeoutSeconds int64 = 300

	// MaxStreamOutputBytes bounds each stdout or stderr log independently.
	MaxStreamOutputBytes int64 = 8_388_608
	// MaxAggregateOutputBytes bounds all stdout and stderr logs in one Run.
	MaxAggregateOutputBytes int64 = 33_554_432

	StdoutResourceLimitFormat     = "checks[%d].stdout exceeded the 8388608-byte safety limit."
	StderrResourceLimitFormat     = "checks[%d].stderr exceeded the 8388608-byte safety limit."
	AggregateResourceLimitMessage = "Verification check logs exceeded the 33554432-byte aggregate safety limit."
	WallClockResourceLimitMessage = "Check run exceeded its wall-clock budget."
	PipeDrainResourceLimitFormat  = "checks[%d] output pipes did not close after process termination."

	processTerminateGrace = 200 * time.Millisecond
	collectorDrainGrace   = time.Second
)

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

// ResourceLimitError reports a bounded runner resource limit. Its public
// message is deterministic; an optional cleanup cause remains available to
// errors.Is/errors.As without changing that message.
type ResourceLimitError struct {
	message string
	cause   error
}

func (e *ResourceLimitError) Error() string { return e.message }
func (e *ResourceLimitError) Unwrap() error { return e.cause }

// RunRooted executes checks while creating every log relative to an already
// opened Evidence root. The caller retains ownership of evidenceRoot. This is
// the publication-safe entry point: it never reconstructs a mutable absolute
// path for logs.
func RunRooted(checks []Definition, repositoryRoot string, evidenceRoot *os.Root) ([]Result, error) {
	return RunRootedContext(context.Background(), checks, repositoryRoot, evidenceRoot)
}

// RunRootedContext is RunRooted with a caller-owned wall-clock boundary. A
// canceled or expired context terminates the current process tree and returns a
// ResourceLimitError. All definitions are validated before any check starts.
func RunRootedContext(
	ctx context.Context,
	checks []Definition,
	repositoryRoot string,
	evidenceRoot *os.Root,
) ([]Result, error) {
	if ctx == nil {
		return nil, infrastructure("Could not use check runner context.", errors.New("nil context"))
	}
	validated, err := validatedDefinitions(checks)
	if err != nil {
		return nil, err
	}
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
	if err := ctx.Err(); err != nil {
		return nil, resourceLimit(WallClockResourceLimitMessage, err)
	}
	return runRootedContext(ctx, validated, workingDirectory, evidenceRoot)
}

type validatedCheck struct {
	definition Definition
	timeout    *big.Int
}

func runRootedContext(
	ctx context.Context,
	checks []validatedCheck,
	workingDirectory string,
	evidenceRoot *os.Root,
) ([]Result, error) {
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
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return nil, infrastructure("Could not open noninteractive check stdin.", err)
	}
	defer stdin.Close()

	results := make([]Result, 0, len(checks))
	budget := &outputBudget{}
	for index, check := range checks {
		if err := ctx.Err(); err != nil {
			return nil, resourceLimit(WallClockResourceLimitMessage, err)
		}
		result, err := runOne(
			ctx,
			check.definition,
			check.timeout,
			index,
			workingDirectory,
			outputDirectory,
			stdin,
			budget,
		)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, resourceLimit(WallClockResourceLimitMessage, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func runOne(
	ctx context.Context,
	definition Definition,
	timeout *big.Int,
	index int,
	workingDirectory string,
	outputDirectory *os.Root,
	stdin *os.File,
	budget *outputBudget,
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
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return Result{}, infrastructure("Could not create check stdout pipe.", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return Result{}, infrastructure("Could not create check stderr pipe.", err)
	}

	events := make(chan struct{}, 2)
	stdoutResult := make(chan collectorResult, 1)
	stderrResult := make(chan collectorResult, 1)
	go func() {
		stdoutResult <- collectOutput(stdoutReader, stdout, outputStdout, index, budget, events)
	}()
	go func() {
		stderrResult <- collectOutput(stderrReader, stderr, outputStderr, index, budget, events)
	}()

	var exitCode *int64
	timedOut := false
	process, launchError := startProcess(
		definition.Argv,
		workingDirectory,
		stdin,
		stdoutWriter,
		stderrWriter,
	)
	stdoutWriteError := stdoutWriter.Close()
	stderrWriteError := stderrWriter.Close()
	pipeCloseError := errors.Join(stdoutWriteError, stderrWriteError)

	var lifecycleError error
	if launchError != nil {
		lifecycleError = pipeCloseError
	} else {
		outcomeChannel := make(chan processOutcome, 1)
		go func() {
			outcomeChannel <- process.wait()
		}()

		outcome, reason, waitError := waitForProcessContext(
			ctx,
			process,
			outcomeChannel,
			time.Duration(timeout.Int64())*time.Second,
			events,
		)
		lifecycleError = errors.Join(pipeCloseError, waitError, process.close())
		timedOut = reason == waitPerCheckTimeout
		code := outcome.exitCode
		exitCode = &code
		if outcome.err != nil {
			lifecycleError = errors.Join(lifecycleError, infrastructure("Could not wait for check process.", outcome.err))
		}
		if reason == waitWallClockLimit {
			lifecycleError = resourceLimit(WallClockResourceLimitMessage, lifecycleError)
		}
	}

	stdoutCollected, stderrCollected, forcedPipeClose := waitForCollectors(
		stdoutReader,
		stderrReader,
		stdoutResult,
		stderrResult,
	)
	if launchError != nil && stdoutCollected.resource == nil && stderrCollected.resource == nil && stdoutCollected.err == nil && stderrCollected.err == nil {
		message := []byte(fmt.Sprintf("Could not start check: %v\n", launchError))
		stderrCollected.resource, stderrCollected.err = writeBounded(
			stderr,
			message,
			outputStderr,
			index,
			&stderrCollected.used,
			budget,
		)
	}
	stdoutCloseError := stdout.Close()
	stderrCloseError := stderr.Close()

	resourceError := firstResourceLimit(stdoutCollected.resource, stderrCollected.resource)
	if existing, ok := lifecycleError.(*ResourceLimitError); ok && resourceError == nil {
		resourceError = existing
		lifecycleError = nil
	}
	if forcedPipeClose && resourceError == nil {
		resourceError = resourceLimit(fmt.Sprintf(PipeDrainResourceLimitFormat, index), nil)
	}
	collectorError := errors.Join(stdoutCollected.err, stderrCollected.err)
	closeError := errors.Join(stdoutCloseError, stderrCloseError)
	if resourceError != nil {
		return Result{}, resourceLimit(resourceError.message, errors.Join(resourceError.cause, lifecycleError, collectorError, closeError))
	}
	if collectorError != nil {
		return Result{}, infrastructure("Could not collect bounded check output.", errors.Join(collectorError, lifecycleError, closeError))
	}
	if lifecycleError != nil {
		return Result{}, infrastructure("Could not finish managed check process.", errors.Join(lifecycleError, closeError))
	}
	if closeError != nil {
		return Result{}, infrastructure("Could not close check logs.", closeError)
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

type waitReason uint8

const (
	waitCompleted waitReason = iota
	waitPerCheckTimeout
	waitCollectorFailure
	waitWallClockLimit
)

func waitForProcessContext(
	ctx context.Context,
	process managedProcess,
	outcomeChannel <-chan processOutcome,
	timeout time.Duration,
	collectorEvents <-chan struct{},
) (processOutcome, waitReason, error) {
	timer := time.NewTimer(timeout)
	defer stopTimer(timer)
	select {
	case outcome := <-outcomeChannel:
		return outcome, waitCompleted, process.cleanupAfterExit()
	case <-collectorEvents:
		outcome, err := process.terminate(outcomeChannel, processTerminateGrace)
		return outcome, waitCollectorFailure, err
	case <-ctx.Done():
		select {
		case outcome := <-outcomeChannel:
			return outcome, waitCompleted, process.cleanupAfterExit()
		default:
		}
		outcome, err := process.terminate(outcomeChannel, processTerminateGrace)
		return outcome, waitWallClockLimit, errors.Join(ctx.Err(), err)
	case <-timer.C:
		select {
		case outcome := <-outcomeChannel:
			return outcome, waitCompleted, process.cleanupAfterExit()
		default:
		}
		outcome, err := process.terminate(outcomeChannel, processTerminateGrace)
		return outcome, waitPerCheckTimeout, err
	}
}

type outputKind uint8

const (
	outputStdout outputKind = iota
	outputStderr
)

type outputBudget struct {
	mu   sync.Mutex
	used int64
}

type collectorResult struct {
	used     int64
	resource *ResourceLimitError
	err      error
}

func waitForCollectors(
	stdoutReader *os.File,
	stderrReader *os.File,
	stdoutResults <-chan collectorResult,
	stderrResults <-chan collectorResult,
) (collectorResult, collectorResult, bool) {
	timer := time.NewTimer(collectorDrainGrace)
	defer stopTimer(timer)
	timerChannel := timer.C
	stdoutChannel := stdoutResults
	stderrChannel := stderrResults
	var stdout collectorResult
	var stderr collectorResult
	forced := false
	for stdoutChannel != nil || stderrChannel != nil {
		select {
		case stdout = <-stdoutChannel:
			stdoutChannel = nil
		case stderr = <-stderrChannel:
			stderrChannel = nil
		case <-timerChannel:
			forced = true
			_ = stdoutReader.Close()
			_ = stderrReader.Close()
			timerChannel = nil
		}
	}
	return stdout, stderr, forced
}

func collectOutput(
	reader *os.File,
	log *os.File,
	kind outputKind,
	checkIndex int,
	budget *outputBudget,
	events chan<- struct{},
) collectorResult {
	defer reader.Close()
	result := collectorResult{}
	buffer := make([]byte, 32*1024)
	for {
		count, readError := reader.Read(buffer)
		if count > 0 && result.resource == nil && result.err == nil {
			limit, writeError := writeBounded(log, buffer[:count], kind, checkIndex, &result.used, budget)
			if limit != nil {
				result.resource = limit
				notifyCollectorEvent(events)
			}
			if writeError != nil {
				result.err = writeError
				notifyCollectorEvent(events)
			}
		}
		if readError != nil {
			if !errors.Is(readError, io.EOF) && !errors.Is(readError, os.ErrClosed) {
				result.err = errors.Join(result.err, readError)
				notifyCollectorEvent(events)
			}
			return result
		}
	}
}

func writeBounded(
	log *os.File,
	contents []byte,
	kind outputKind,
	checkIndex int,
	streamUsed *int64,
	budget *outputBudget,
) (*ResourceLimitError, error) {
	allowed, limit := budget.reserve(kind, checkIndex, *streamUsed, int64(len(contents)))
	if allowed > 0 {
		written, err := log.Write(contents[:allowed])
		*streamUsed += int64(written)
		if err != nil {
			return nil, err
		}
		if int64(written) != allowed {
			return nil, io.ErrShortWrite
		}
	}
	return limit, nil
}

func (budget *outputBudget) reserve(
	kind outputKind,
	checkIndex int,
	streamUsed int64,
	requested int64,
) (int64, *ResourceLimitError) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	streamRemaining := MaxStreamOutputBytes - streamUsed
	aggregateRemaining := MaxAggregateOutputBytes - budget.used
	allowed := requested
	if allowed > streamRemaining {
		allowed = streamRemaining
	}
	if allowed > aggregateRemaining {
		allowed = aggregateRemaining
	}
	if allowed < 0 {
		allowed = 0
	}
	budget.used += allowed
	if allowed == requested {
		return allowed, nil
	}
	if streamRemaining <= aggregateRemaining {
		return allowed, resourceLimit(outputLimitMessage(kind, checkIndex), nil)
	}
	return allowed, resourceLimit(AggregateResourceLimitMessage, nil)
}

func outputLimitMessage(kind outputKind, checkIndex int) string {
	if kind == outputStderr {
		return fmt.Sprintf(StderrResourceLimitFormat, checkIndex)
	}
	return fmt.Sprintf(StdoutResourceLimitFormat, checkIndex)
}

func notifyCollectorEvent(events chan<- struct{}) {
	select {
	case events <- struct{}{}:
	default:
	}
}

func firstResourceLimit(values ...*ResourceLimitError) *ResourceLimitError {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
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

func validatedDefinitions(values []Definition) ([]validatedCheck, error) {
	validated := make([]validatedCheck, len(values))
	for index, value := range values {
		definition, timeout, err := validatedDefinition(value, index)
		if err != nil {
			return nil, err
		}
		validated[index] = validatedCheck{definition: definition, timeout: timeout}
	}
	return validated, nil
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
	if timeout.Cmp(big.NewInt(MaxTimeoutSeconds)) > 0 {
		return Definition{}, nil, &DefinitionError{message: fmt.Sprintf(
			"checks[%d].timeout_seconds must be at most %d seconds.",
			index,
			MaxTimeoutSeconds,
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

func resourceLimit(message string, cause error) *ResourceLimitError {
	return &ResourceLimitError{message: message, cause: cause}
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
