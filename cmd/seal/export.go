package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jgoneit/seal/internal/runstate"
)

const exportHelp = `usage: seal run export --format json

Export every stored Run in this repository as seal-run-export/v1 JSON.
The command validates saved records without executing checks, observing current
source, or writing state. Historical completion records are not current Acceptance.
Exit 8 includes partial JSON with static issues; exit 0 is a complete scan.
`

func exportHelpRequested(args []string) bool {
	return len(args) == 3 && args[0] == "run" && args[1] == "export" &&
		(args[2] == "--help" || args[2] == "-h")
}

func exportRuns(cwd string, args []string, stdout, stderr io.Writer) int {
	if !(len(args) == 2 && args[0] == "--format" && args[1] == "json") &&
		!(len(args) == 1 && args[0] == "--format=json") {
		fmt.Fprint(stderr, "error: run export requires --format json\n"+exportHelp)
		return 2
	}
	report, err := runstate.ExportRuns(cwd, version)
	if err != nil {
		fmt.Fprintln(stderr, "error: could not read repository for Run export")
		if runstate.KindOf(err) == runstate.KindRepository {
			return 3
		}
		return 1
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "error: could not render Run export")
		return 1
	}
	if _, err := stdout.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintln(stderr, "error: could not write Run export")
		return 1
	}
	if !report.ScanComplete {
		return 8
	}
	return 0
}
