package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jgoneit/seal/internal/runstate"
	"github.com/jgoneit/seal/internal/taskstate"
)

const version = "0.0.0-dev"

const help = `Seal exposes deterministic Acceptance state.

Usage:
  seal --help
  seal --version
  seal task show <TASK_ID>
  seal run show <TASK_ID> --run-id <RUN_ID>

Options:
  --help     Show this help text.
  --version  Print the development version.

The Task and Run queries are exact-identity, read-only compatibility commands.
Task creation, verification, completion, and latest-id selection are unsupported.
`

func main() {
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not resolve the working directory: %v\n", err)
		os.Exit(3)
	}
	os.Exit(runCLI(workingDirectory, os.Args[1:], os.Stdout, os.Stderr))
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

	if len(args) == 3 && args[0] == "task" && args[1] == "show" {
		return showTask(cwd, args[2], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "run" && args[1] == "show" {
		taskID, runID, err := parseRunShow(args[2:])
		if err != nil {
			return commandUsage(stderr, err.Error())
		}
		return showRun(cwd, taskID, runID, stdout, stderr)
	}

	return commandUsage(stderr, "expected --help, --version, task show, or run show")
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

func commandUsage(stderr io.Writer, detail string) int {
	fmt.Fprintf(stderr, "error: %s\n", detail)
	fmt.Fprintln(stderr, "usage: seal task show <TASK_ID>")
	fmt.Fprintln(stderr, "       seal run show <TASK_ID> --run-id <RUN_ID>")
	return 2
}
