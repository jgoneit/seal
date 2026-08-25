package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jgoneit/seal/internal/runstate"
	"github.com/jgoneit/seal/internal/taskstate"
)

const version = "0.3.0-rc.2"

const help = `Seal exposes deterministic Acceptance state.

Usage:
  seal --help
  seal --version
  seal task create --file <TASK_JSON> [--force]
  seal task show <TASK_ID>
  seal verify <TASK_ID>
  seal run show <TASK_ID> --run-id <RUN_ID>
  seal complete <TASK_ID> --run-id <RUN_ID>

Options:
  --file <TASK_JSON>  Read a Task Spec from this file.
  --force             Replace the exact existing Task snapshot.
  --help              Show this help text.
  --version           Print the Seal version.

Task creation validates and stores one normalized Task snapshot.
Verification records one exact-identity, manifest-valid Evidence Run.
The Task and Run queries are exact-identity, read-only compatibility commands.
Completion evaluates one exact manifest-valid Run against current source.
Latest-id selection is unsupported.
`

const taskCreateHelp = `usage: seal task create [-h] --file FILE [--force]

options:
  -h, --help   show this help message and exit
  --file FILE  path to a Task Spec JSON file
  --force      replace an existing Task snapshot
`

const verifyHelp = `usage: seal verify [-h] TASK_ID

positional arguments:
  TASK_ID

options:
  -h, --help  show this help message and exit
`

const completeHelp = `usage: seal complete [-h] --run-id RUN_ID TASK_ID

positional arguments:
  TASK_ID

options:
  -h, --help       show this help message and exit
  --run-id RUN_ID  explicit verification run id to validate; latest-run
                   selection is unsupported
`

func main() {
	configureProcessSignals()
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	if isInformationalCommand(args) {
		return runCLI("", args, stdout, stderr)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		// Keep argument and identity validation ahead of repository failure.
		return runCLI("", args, stdout, stderr)
	}
	return runCLI(workingDirectory, args, stdout, stderr)
}

func isInformationalCommand(args []string) bool {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "--version") {
		return true
	}
	return taskCreateHelpRequested(args) || verifyHelpRequested(args) || completeHelpRequested(args)
}

func runCLI(cwd string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "--help":
			fmt.Fprint(stdout, help)
			return 0
		case "--version":
			fmt.Fprintln(stdout, version)
			return 0
		}
	}
	if taskCreateHelpRequested(args) {
		fmt.Fprint(stdout, taskCreateHelp)
		return 0
	}
	if verifyHelpRequested(args) {
		fmt.Fprint(stdout, verifyHelp)
		return 0
	}
	if completeHelpRequested(args) {
		fmt.Fprint(stdout, completeHelp)
		return 0
	}

	if len(args) == 3 && args[0] == "task" && args[1] == "show" {
		return showTask(cwd, args[2], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "task" && args[1] == "create" {
		taskFile, force, err := parseTaskCreate(args[2:])
		if err != nil {
			return commandUsage(stderr, err.Error())
		}
		return createTask(cwd, taskFile, force, stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "verify" {
		taskID, err := parseVerify(args[1:])
		if err != nil {
			return commandUsage(stderr, err.Error())
		}
		return verifyTask(cwd, taskID, stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "run" && args[1] == "show" {
		taskID, runID, err := parseRunShow(args[2:])
		if err != nil {
			return commandUsage(stderr, err.Error())
		}
		return showRun(cwd, taskID, runID, stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "complete" {
		taskID, runID, err := parseComplete(args[1:])
		if err != nil {
			return commandUsage(stderr, err.Error())
		}
		return completeTask(cwd, taskID, runID, stdout, stderr)
	}

	return commandUsage(stderr, "expected --help, --version, task create, task show, verify, run show, or complete")
}

func completeTask(cwd, taskID, runID string, stdout, stderr io.Writer) int {
	completed, err := runstate.Complete(cwd, taskID, runID)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if code, ok := runstate.CompletionExitCode(err); ok {
			return code
		}
		return 1
	}
	encoded, err := completed.ReferenceJSON()
	if err != nil {
		fmt.Fprintf(stderr, "error: could not render Completion output: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "error: could not write Completion output: %v\n", err)
		return 1
	}
	return 0
}

func verifyTask(cwd, taskID string, stdout, stderr io.Writer) int {
	run, err := runstate.Verify(cwd, taskID)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		switch runstate.KindOf(err) {
		case runstate.KindRuntime:
			return 1
		case runstate.KindRepository:
			return 3
		default:
			return 2
		}
	}
	encoded, err := run.ReferenceJSON()
	if err != nil {
		fmt.Fprintf(stderr, "error: could not render verification output: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "error: could not write verification output: %v\n", err)
		return 1
	}
	return 0
}

func createTask(cwd, taskFile string, force bool, stdout, stderr io.Writer) int {
	encoded, err := taskstate.Create(cwd, taskFile, force)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if kind, ok := taskstate.KindOf(err); ok {
			switch kind {
			case taskstate.EncodingFailure, taskstate.NumericFailure, taskstate.NestingLimitFailure:
				return 1
			case taskstate.Repository:
				return 3
			}
		}
		return 2
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintf(stderr, "error: could not write Task output: %v\n", err)
		return 1
	}
	return 0
}

func showTask(cwd, taskID string, stdout, stderr io.Writer) int {
	document, err := taskstate.Show(cwd, taskID)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		kind, ok := taskstate.KindOf(err)
		if ok {
			switch kind {
			case taskstate.EncodingFailure, taskstate.NumericFailure, taskstate.NestingLimitFailure:
				return 1
			case taskstate.Repository:
				return 3
			}
		}
		return 2
	}

	encoded, err := taskstate.Render(document)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not render stored Task JSON: %v\n", err)
		if kind, ok := taskstate.KindOf(err); ok && kind == taskstate.EncodingFailure {
			return 1
		}
		return 2
	}
	if _, err := stdout.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintf(stderr, "error: could not write Task output: %v\n", err)
		return 1
	}
	return 0
}

func showRun(cwd, taskID, runID string, stdout, stderr io.Writer) int {
	validated, err := runstate.ValidateRun(cwd, taskID, runID)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		switch runstate.KindOf(err) {
		case runstate.KindRuntime:
			return 1
		case runstate.KindRepository:
			return 3
		case runstate.KindEvidence:
			return 8
		default:
			return 2
		}
	}

	encoded, err := validated.Summary().ReferenceJSON()
	if err != nil {
		fmt.Fprintf(stderr, "error: could not render Run output: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintf(stderr, "error: could not write Run output: %v\n", err)
		return 1
	}
	return 0
}

func parseRunShow(args []string) (string, string, error) {
	var taskID string
	var runID string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--run-id":
			if index+1 >= len(args) {
				return "", "", fmt.Errorf("run show requires --run-id <RUN_ID>")
			}
			index++
			runID = args[index]
		case strings.HasPrefix(argument, "--run-id="):
			runID = strings.TrimPrefix(argument, "--run-id=")
		case strings.HasPrefix(argument, "-"):
			return "", "", fmt.Errorf("run show received an unsupported option %q", argument)
		case taskID == "":
			taskID = argument
		default:
			return "", "", fmt.Errorf("run show requires exactly one <TASK_ID>")
		}
	}
	if taskID == "" {
		return "", "", fmt.Errorf("run show requires exactly one <TASK_ID>")
	}
	if runID == "" {
		return "", "", fmt.Errorf("run show requires --run-id <RUN_ID>")
	}
	return taskID, runID, nil
}

func parseComplete(args []string) (string, string, error) {
	var taskID string
	taskIDSeen := false
	var runID string
	runIDSeen := false
	optionParsing := true
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case optionParsing && argument == "--":
			optionParsing = false
		case optionParsing && argument == "--run-id":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") && args[index+1] != "-" {
				return "", "", fmt.Errorf("complete requires --run-id <RUN_ID>")
			}
			index++
			runID = args[index]
			runIDSeen = true
		case optionParsing && strings.HasPrefix(argument, "--run-id="):
			runID = strings.TrimPrefix(argument, "--run-id=")
			runIDSeen = true
		case optionParsing && strings.HasPrefix(argument, "-"):
			return "", "", fmt.Errorf("complete received an unsupported option %q", argument)
		case !taskIDSeen:
			taskID = argument
			taskIDSeen = true
		default:
			return "", "", fmt.Errorf("complete requires exactly one <TASK_ID>")
		}
	}
	if !taskIDSeen {
		return "", "", fmt.Errorf("complete requires exactly one <TASK_ID>")
	}
	if !runIDSeen {
		return "", "", fmt.Errorf("complete requires --run-id <RUN_ID>")
	}
	return taskID, runID, nil
}

func parseVerify(args []string) (string, error) {
	positional := make([]string, 0, len(args))
	optionParsing := true
	for _, argument := range args {
		if optionParsing && argument == "--" {
			optionParsing = false
			continue
		}
		if optionParsing && strings.HasPrefix(argument, "-") {
			return "", fmt.Errorf("verify received an unsupported option %q", argument)
		}
		positional = append(positional, argument)
	}
	if len(positional) != 1 {
		return "", fmt.Errorf("verify requires exactly one <TASK_ID>")
	}
	return positional[0], nil
}

func parseTaskCreate(args []string) (string, bool, error) {
	var taskFile string
	fileSeen := false
	force := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--file":
			if index+1 >= len(args) || (strings.HasPrefix(args[index+1], "-") && args[index+1] != "-") {
				return "", false, fmt.Errorf("task create requires --file <TASK_JSON>")
			}
			index++
			taskFile = args[index]
			fileSeen = true
		case strings.HasPrefix(argument, "--file="):
			taskFile = strings.TrimPrefix(argument, "--file=")
			fileSeen = true
		case argument == "--force":
			force = true
		case strings.HasPrefix(argument, "-"):
			return "", false, fmt.Errorf("task create received an unsupported option %q", argument)
		default:
			return "", false, fmt.Errorf("task create does not accept positional argument %q", argument)
		}
	}
	if !fileSeen {
		return "", false, fmt.Errorf("task create requires --file <TASK_JSON>")
	}
	return taskFile, force, nil
}

func taskCreateHelpRequested(args []string) bool {
	if len(args) < 3 || args[0] != "task" || args[1] != "create" {
		return false
	}
	for index := 2; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--file":
			if index+1 >= len(args) || (strings.HasPrefix(args[index+1], "-") && args[index+1] != "-") {
				return false
			}
			index++
		case strings.HasPrefix(argument, "--file="):
			continue
		case argument == "--help" || argument == "-h":
			return true
		}
	}
	return false
}

func verifyHelpRequested(args []string) bool {
	if len(args) < 2 || args[0] != "verify" {
		return false
	}
	for _, argument := range args[1:] {
		if argument == "--" {
			return false
		}
		if argument == "--help" || argument == "-h" {
			return true
		}
	}
	return false
}

func completeHelpRequested(args []string) bool {
	if len(args) < 2 || args[0] != "complete" {
		return false
	}
	for index := 1; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--":
			return false
		case argument == "--run-id":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") && args[index+1] != "-" {
				return false
			}
			index++
		case strings.HasPrefix(argument, "--run-id="):
			continue
		case argument == "--help" || argument == "-h":
			return true
		}
	}
	return false
}

func commandUsage(stderr io.Writer, detail string) int {
	fmt.Fprintf(stderr, "error: %s\n", detail)
	fmt.Fprintln(stderr, "usage: seal task create --file <TASK_JSON> [--force]")
	fmt.Fprintln(stderr, "       seal task show <TASK_ID>")
	fmt.Fprintln(stderr, "       seal verify <TASK_ID>")
	fmt.Fprintln(stderr, "       seal run show <TASK_ID> --run-id <RUN_ID>")
	fmt.Fprintln(stderr, "       seal complete <TASK_ID> --run-id <RUN_ID>")
	return 2
}
