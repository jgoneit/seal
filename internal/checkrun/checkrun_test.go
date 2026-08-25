package checkrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunRecordsReferenceContractAndContinues(t *testing.T) {
	repository := t.TempDir()
	resolvedRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	orderPath := filepath.Join(t.TempDir(), "order.txt")
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")
	t.Setenv("SEAL_CHECKRUN_INHERITED", "inherited-value")

	checks := []Definition{
		{
			Name:     "first failure",
			Argv:     helperArgv("mark", orderPath, "first", "17"),
			Required: true,
		},
		{
			Name:           "check / weird✨",
			Argv:           helperArgv("observe", orderPath, "second", "; touch should-not-exist"),
			Required:       false,
			TimeoutSeconds: big.NewInt(9),
		},
		{
			Name:     "missing executable",
			Argv:     []string{filepath.Join(repository, "definitely-missing-check")},
			Required: true,
		},
		{
			Name:     "last success",
			Argv:     helperArgv("mark", orderPath, "last", "0"),
			Required: true,
		},
	}

	results, err := RunRooted(checks, repository, evidenceRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("result count = %d, want 4", len(results))
	}
	if got := readFile(t, orderPath); got != "first\nsecond\nlast\n" {
		t.Fatalf("saved execution order = %q", got)
	}

	assertExitCode(t, results[0].ExitCode, 17)
	if results[0].Passed || results[0].TimedOut {
		t.Fatalf("failed result flags = passed:%v timed_out:%v", results[0].Passed, results[0].TimedOut)
	}
	if results[0].EffectiveTimeout.String() != "300" {
		t.Fatalf("default timeout = %s", results[0].EffectiveTimeout)
	}

	second := results[1]
	assertExitCode(t, second.ExitCode, 0)
	if !second.Passed || second.Required || second.TimedOut {
		t.Fatalf("optional result flags = passed:%v required:%v timed_out:%v", second.Passed, second.Required, second.TimedOut)
	}
	if second.EffectiveTimeout.String() != "9" {
		t.Fatalf("explicit timeout = %s", second.EffectiveTimeout)
	}
	if second.CWD != resolvedRepository {
		t.Fatalf("cwd = %q, want %q", second.CWD, resolvedRepository)
	}
	if second.StdoutPath != "checks/001-check_weird-f68b5d498a07.stdout" {
		t.Fatalf("stdout path = %q", second.StdoutPath)
	}
	if second.StderrPath != "checks/001-check_weird-f68b5d498a07.stderr" {
		t.Fatalf("stderr path = %q", second.StderrPath)
	}
	var observation struct {
		Arguments []string `json:"arguments"`
		CWD       string   `json:"cwd"`
		Inherited string   `json:"inherited"`
	}
	decodeJSON(t, filepath.Join(evidence, filepath.FromSlash(second.StdoutPath)), &observation)
	if observation.CWD != resolvedRepository || observation.Inherited != "inherited-value" {
		t.Fatalf("child observation = %#v", observation)
	}
	if !reflect.DeepEqual(observation.Arguments, []string{"; touch should-not-exist"}) {
		t.Fatalf("literal argv = %#v", observation.Arguments)
	}
	if _, err := os.Stat(filepath.Join(repository, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell metacharacter was not literal: %v", err)
	}

	third := results[2]
	if third.ExitCode != nil || third.Passed || third.TimedOut {
		t.Fatalf("launch failure = %#v", third)
	}
	launchStderr := readFile(t, filepath.Join(evidence, filepath.FromSlash(third.StderrPath)))
	if !strings.HasPrefix(launchStderr, "Could not start check: ") || !strings.HasSuffix(launchStderr, "\n") {
		t.Fatalf("launch stderr = %q", launchStderr)
	}
	assertExitCode(t, results[3].ExitCode, 0)
	if !results[3].Passed {
		t.Fatal("last check did not run after launch failure")
	}

	timestampPattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)
	for index, result := range results {
		if !timestampPattern.MatchString(result.StartedAt) || !timestampPattern.MatchString(result.FinishedAt) {
			t.Fatalf("result[%d] timestamps = %q / %q", index, result.StartedAt, result.FinishedAt)
		}
		if result.DurationSeconds < 0 {
			t.Fatalf("result[%d] negative duration", index)
		}
		assertResultJSONFields(t, result)
	}
}

func TestRunRootedKeepsLogsInRetainedEvidenceDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory-handle rename fixture is POSIX-only")
	}
	repository := t.TempDir()
	rootDirectory := t.TempDir()
	evidence := privateDirectoryAt(t, filepath.Join(rootDirectory, "evidence"))
	root, err := os.OpenRoot(evidence)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	moved := filepath.Join(rootDirectory, "retained")
	if err := os.Rename(evidence, moved); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(rootDirectory, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, evidence); err != nil {
		t.Fatal(err)
	}

	results, err := RunRooted([]Definition{{
		Name:     "missing",
		Argv:     []string{filepath.Join(repository, "missing-executable")},
		Required: true,
	}}, repository, root)
	if err != nil {
		t.Fatalf("RunRooted(): %v", err)
	}
	if len(results) != 1 || results[0].ExitCode != nil || results[0].Passed {
		t.Fatalf("results = %#v", results)
	}
	if _, err := os.Stat(filepath.Join(moved, filepath.FromSlash(results[0].StderrPath))); err != nil {
		t.Fatalf("retained-root stderr missing: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("outside path changed: entries %v, error %v", entries, err)
	}
}

func TestRunUsesClosedStdinAndPreservesBoundedRawLogs(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")

	inputPath := filepath.Join(t.TempDir(), "stdin.bin")
	input := []byte{'i', 'n', 0, 0xff, '\n'}
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	previousStdin := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = previousStdin }()

	results, err := RunRooted([]Definition{
		{Name: "stdin", Argv: helperArgv("copy-stdin"), Required: true},
		{Name: "large", Argv: helperArgv("large", strconv.Itoa(2*1024*1024)), Required: true},
	}, repository, evidenceRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readBytes(t, filepath.Join(evidence, filepath.FromSlash(results[0].StdoutPath))); len(got) != 0 {
		t.Fatalf("closed stdin produced bytes %v", got)
	}
	large := readBytes(t, filepath.Join(evidence, filepath.FromSlash(results[1].StdoutPath)))
	if len(large) != 2*1024*1024 {
		t.Fatalf("large log size = %d", len(large))
	}
	if large[0] != 0xff || large[len(large)-1] != 0xff {
		t.Fatal("large raw log content changed")
	}
	stderr := readBytes(t, filepath.Join(evidence, filepath.FromSlash(results[1].StderrPath)))
	if !bytes.Equal(stderr, []byte{0, 0xfe, '\n'}) {
		t.Fatalf("raw stderr = %v", stderr)
	}

	if runtime.GOOS != "windows" {
		assertPermissions(t, filepath.Join(evidence, "checks"), 0o700)
		for _, result := range results {
			assertPermissions(t, filepath.Join(evidence, filepath.FromSlash(result.StdoutPath)), 0o600)
			assertPermissions(t, filepath.Join(evidence, filepath.FromSlash(result.StderrPath)), 0o600)
		}
	}
}

func TestRunAcceptsMaximumTimeout(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")
	maximum := big.NewInt(MaxTimeoutSeconds)

	results, err := RunRooted([]Definition{{
		Name:           "maximum timeout",
		Argv:           helperArgv("exit", "0"),
		Required:       true,
		TimeoutSeconds: maximum,
	}}, repository, evidenceRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].EffectiveTimeout.Cmp(maximum) != 0 {
		t.Fatalf("timeout = %s", results[0].EffectiveTimeout)
	}
	encoded, err := json.Marshal(results[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `"effective_timeout":` + maximum.String()
	if !bytes.Contains(encoded, []byte(want)) {
		t.Fatalf("encoded timeout is not an exact JSON integer: %s", encoded)
	}
}

func TestRunRejectsInvalidDefinitions(t *testing.T) {
	repository := t.TempDir()
	tests := []struct {
		name       string
		definition Definition
		message    string
	}{
		{name: "name", definition: Definition{Argv: []string{"ok"}}, message: "checks[0].name must be a non-empty string."},
		{name: "argv", definition: Definition{Name: "bad"}, message: "checks[0].argv must be a non-empty array."},
		{name: "argument", definition: Definition{Name: "bad", Argv: []string{"ok", ""}}, message: "checks[0].argv[1] must be a non-empty string."},
		{name: "zero timeout", definition: Definition{Name: "bad", Argv: []string{"ok"}, TimeoutSeconds: big.NewInt(0)}, message: "checks[0].timeout_seconds must be a positive integer."},
		{name: "negative timeout", definition: Definition{Name: "bad", Argv: []string{"ok"}, TimeoutSeconds: big.NewInt(-1)}, message: "checks[0].timeout_seconds must be a positive integer."},
		{name: "over maximum timeout", definition: Definition{Name: "bad", Argv: []string{"ok"}, TimeoutSeconds: big.NewInt(MaxTimeoutSeconds + 1)}, message: "checks[0].timeout_seconds must be at most 300 seconds."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidenceRoot := openTestRoot(t, privateTempDirectory(t))
			_, err := RunRooted([]Definition{test.definition}, repository, evidenceRoot)
			var definitionError *DefinitionError
			if !errors.As(err, &definitionError) {
				t.Fatalf("error type = %T, want DefinitionError (%v)", err, err)
			}
			if err.Error() != test.message {
				t.Fatalf("error = %q, want %q", err, test.message)
			}
		})
	}
}

func TestRunPrevalidatesEveryDefinitionBeforeExecution(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	marker := filepath.Join(t.TempDir(), "executed")
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")

	_, err := RunRooted([]Definition{
		{Name: "would execute", Argv: helperArgv("mark", marker, "ran", "0"), Required: true},
		{Name: "invalid later definition", Argv: helperArgv("exit", "0"), Required: true, TimeoutSeconds: big.NewInt(MaxTimeoutSeconds + 1)},
	}, repository, evidenceRoot)
	var definitionError *DefinitionError
	if !errors.As(err, &definitionError) {
		t.Fatalf("error type = %T, want DefinitionError (%v)", err, err)
	}
	if got := err.Error(); got != "checks[1].timeout_seconds must be at most 300 seconds." {
		t.Fatalf("error = %q", got)
	}
	if _, statError := os.Stat(marker); !errors.Is(statError, os.ErrNotExist) {
		t.Fatalf("earlier check executed: %v", statError)
	}
	if _, statError := os.Stat(filepath.Join(evidence, "checks")); !errors.Is(statError, os.ErrNotExist) {
		t.Fatalf("definition failure created logs: %v", statError)
	}
}

func TestRunEnforcesPerStreamOutputLimitAtCapPlusOne(t *testing.T) {
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")
	for _, stream := range []struct {
		name      string
		extension string
		message   string
	}{
		{name: "stdout", extension: "stdout", message: fmt.Sprintf(StdoutResourceLimitFormat, 0)},
		{name: "stderr", extension: "stderr", message: fmt.Sprintf(StderrResourceLimitFormat, 0)},
	} {
		t.Run(stream.name, func(t *testing.T) {
			repository := t.TempDir()
			exactEvidence := privateTempDirectory(t)
			exactRoot := openTestRoot(t, exactEvidence)
			exactName := "exact " + stream.name
			results, err := RunRooted([]Definition{{
				Name:     exactName,
				Argv:     helperArgv("stream", stream.name, strconv.FormatInt(MaxStreamOutputBytes, 10)),
				Required: true,
			}}, repository, exactRoot)
			if err != nil {
				t.Fatalf("exact cap: %v", err)
			}
			if len(results) != 1 || !results[0].Passed {
				t.Fatalf("exact cap result = %#v", results)
			}
			assertFileSize(t, filepath.Join(exactEvidence, "checks", outputStem(0, exactName)+"."+stream.extension), MaxStreamOutputBytes)

			overflowEvidence := privateTempDirectory(t)
			overflowRoot := openTestRoot(t, overflowEvidence)
			overflowName := "overflow " + stream.name
			_, err = RunRooted([]Definition{{
				Name:     overflowName,
				Argv:     helperArgv("stream", stream.name, strconv.FormatInt(MaxStreamOutputBytes+1, 10)),
				Required: true,
			}}, repository, overflowRoot)
			assertResourceLimit(t, err, stream.message)
			assertFileSize(t, filepath.Join(overflowEvidence, "checks", outputStem(0, overflowName)+"."+stream.extension), MaxStreamOutputBytes)
		})
	}
}

func TestRunEnforcesSharedAggregateOutputLimit(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	evidenceRoot := openTestRoot(t, evidence)
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")
	checks := []Definition{
		{Name: "first full pair", Argv: helperArgv("both-streams", strconv.FormatInt(MaxStreamOutputBytes, 10)), Required: true},
		{Name: "second full pair", Argv: helperArgv("both-streams", strconv.FormatInt(MaxStreamOutputBytes, 10)), Required: true},
		{Name: "aggregate plus one", Argv: helperArgv("stream", "stdout", "1"), Required: true},
	}

	_, err := RunRooted(checks, repository, evidenceRoot)
	assertResourceLimit(t, err, AggregateResourceLimitMessage)
	var total int64
	for index, check := range checks {
		for _, extension := range []string{"stdout", "stderr"} {
			path := filepath.Join(evidence, "checks", outputStem(index, check.Name)+"."+extension)
			info, statError := os.Stat(path)
			if statError != nil {
				t.Fatal(statError)
			}
			total += info.Size()
		}
	}
	if total != MaxAggregateOutputBytes {
		t.Fatalf("aggregate logs = %d, want %d", total, MaxAggregateOutputBytes)
	}
}

func TestRunRootedContextTerminatesAtWallClockDeadline(t *testing.T) {
	repository := t.TempDir()
	evidenceRoot := openTestRoot(t, privateTempDirectory(t))
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := RunRootedContext(ctx, []Definition{{
		Name:     "context deadline",
		Argv:     helperArgv("block"),
		Required: true,
	}}, repository, evidenceRoot)
	assertResourceLimit(t, err, WallClockResourceLimitMessage)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("wall-clock limit returned after %s", elapsed)
	}
}

func TestRunReportsInfrastructureFault(t *testing.T) {
	repository := t.TempDir()
	evidence := privateTempDirectory(t)
	checksDirectory := filepath.Join(evidence, "checks")
	if err := os.Mkdir(checksDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(checksDirectory, outputStem(0, "blocked")+".stdout")
	if err := os.Mkdir(stdoutPath, 0o700); err != nil {
		t.Fatal(err)
	}
	evidenceRoot := openTestRoot(t, evidence)

	_, err := RunRooted([]Definition{{Name: "blocked", Argv: []string{"unused"}}}, repository, evidenceRoot)
	var infrastructureError *InfrastructureError
	if !errors.As(err, &infrastructureError) {
		t.Fatalf("error type = %T, want InfrastructureError (%v)", err, err)
	}
}

func TestConcurrentRunsUseIsolatedLogs(t *testing.T) {
	repository := t.TempDir()
	t.Setenv("SEAL_CHECKRUN_HELPER", "1")
	const runCount = 8
	evidenceRoots := make([]string, runCount)
	fixtureRoot := t.TempDir()
	for index := range evidenceRoots {
		evidenceRoots[index] = privateDirectoryAt(t, filepath.Join(fixtureRoot, fmt.Sprintf("run-%d", index)))
	}
	var wait sync.WaitGroup
	errorsChannel := make(chan error, runCount)
	for index := 0; index < runCount; index++ {
		index := index
		evidence := evidenceRoots[index]
		evidenceRoot := openTestRoot(t, evidence)
		wait.Add(1)
		go func() {
			defer wait.Done()
			results, err := RunRooted([]Definition{{
				Name:     "concurrent",
				Argv:     helperArgv("text", strconv.Itoa(index)),
				Required: true,
			}}, repository, evidenceRoot)
			if err != nil {
				errorsChannel <- err
				return
			}
			got, err := os.ReadFile(filepath.Join(evidence, filepath.FromSlash(results[0].StdoutPath)))
			if err != nil {
				errorsChannel <- err
				return
			}
			if string(got) != strconv.Itoa(index) {
				errorsChannel <- fmt.Errorf("run %d log = %q", index, got)
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func TestOutputStemMatchesReference(t *testing.T) {
	tests := []struct {
		index int
		name  string
		want  string
	}{
		{0, "check / weird✨", "000-check_weird-f68b5d498a07"},
		{0, "...---", "000-check-8d4e47cef93b"},
		{0, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "000-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV-540363d1071a"},
		{1000, "ok", "1000-ok-2689367b205c"},
	}
	for _, test := range tests {
		if got := outputStem(test.index, test.name); got != test.want {
			t.Errorf("outputStem(%d, %q) = %q, want %q", test.index, test.name, got, test.want)
		}
	}
}

func TestCheckrunHelperProcess(t *testing.T) {
	if os.Getenv("SEAL_CHECKRUN_HELPER") != "1" {
		return
	}
	arguments := helperArguments()
	if len(arguments) == 0 {
		os.Exit(97)
	}
	switch arguments[0] {
	case "mark":
		if len(arguments) != 4 {
			os.Exit(96)
		}
		file, err := os.OpenFile(arguments[1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(95)
		}
		_, _ = fmt.Fprintln(file, arguments[2])
		_ = file.Close()
		code, _ := strconv.Atoi(arguments[3])
		os.Exit(code)
	case "observe":
		file, err := os.OpenFile(arguments[1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(94)
		}
		_, _ = fmt.Fprintln(file, arguments[2])
		_ = file.Close()
		cwd, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"arguments": arguments[3:],
			"cwd":       cwd,
			"inherited": os.Getenv("SEAL_CHECKRUN_INHERITED"),
		})
		os.Exit(0)
	case "copy-stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "large":
		size, _ := strconv.Atoi(arguments[1])
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{0xff}, size))
		_, _ = os.Stderr.Write([]byte{0, 0xfe, '\n'})
		os.Exit(0)
	case "stream":
		size, _ := strconv.Atoi(arguments[2])
		destination := os.Stdout
		if arguments[1] == "stderr" {
			destination = os.Stderr
		}
		_, _ = destination.Write(bytes.Repeat([]byte{'x'}, size))
		os.Exit(0)
	case "both-streams":
		size, _ := strconv.Atoi(arguments[1])
		contents := bytes.Repeat([]byte{'x'}, size)
		_, _ = os.Stdout.Write(contents)
		_, _ = os.Stderr.Write(contents)
		os.Exit(0)
	case "block":
		for {
			time.Sleep(time.Hour)
		}
	case "exit":
		code, _ := strconv.Atoi(arguments[1])
		os.Exit(code)
	case "text":
		_, _ = io.WriteString(os.Stdout, arguments[1])
		os.Exit(0)
	default:
		os.Exit(93)
	}
}

func helperArgv(mode string, arguments ...string) []string {
	argv := []string{os.Args[0], "-test.run=^TestCheckrunHelperProcess$", "--", mode}
	return append(argv, arguments...)
}

func helperArguments() []string {
	for index, argument := range os.Args {
		if argument == "--" {
			return os.Args[index+1:]
		}
	}
	return nil
}

func privateTempDirectory(t *testing.T) string {
	t.Helper()
	return privateDirectoryAt(t, filepath.Join(t.TempDir(), "evidence"))
}

func openTestRoot(t *testing.T, directory string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func privateDirectoryAt(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	return string(readBytes(t, path))
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func decodeJSON(t *testing.T, path string, destination any) {
	t.Helper()
	contents := readBytes(t, path)
	if err := json.Unmarshal(contents, destination); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, contents)
	}
}

func assertExitCode(t *testing.T, actual *int64, want int64) {
	t.Helper()
	if actual == nil || *actual != want {
		t.Fatalf("exit code = %v, want %d", actual, want)
	}
}

func assertResourceLimit(t *testing.T, err error, message string) {
	t.Helper()
	var limit *ResourceLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("error type = %T, want ResourceLimitError (%v)", err, err)
	}
	if got := err.Error(); got != message {
		t.Fatalf("resource error = %q, want %q", got, message)
	}
}

func assertFileSize(t *testing.T, path string, want int64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != want {
		t.Fatalf("%s size = %d, want %d", path, info.Size(), want)
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %#o, want %#o", path, got, want)
	}
}

func assertResultJSONFields(t *testing.T, result Result) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"argv", "cwd", "duration_seconds", "effective_timeout", "exit_code",
		"finished_at", "name", "passed", "required", "started_at",
		"stderr_path", "stdout_path", "timed_out",
	}
	if len(document) != len(want) {
		t.Fatalf("result fields = %v", document)
	}
	for _, field := range want {
		if _, ok := document[field]; !ok {
			t.Fatalf("result missing %q: %s", field, encoded)
		}
	}
}
