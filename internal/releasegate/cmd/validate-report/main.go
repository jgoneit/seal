package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jgoneit/seal/internal/releasegate"
)

func main() {
	os.Exit(run())
}

func run() int {
	flags := flag.NewFlagSet("validate-report", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	repository := flags.String("repository", ".", "repository containing the release tag")
	reportsDirectory := flags.String("reports-dir", "release/acceptance", "acceptance report directory")
	releaseTag := flags.String("release-tag", "", "RC or stable release tag to validate")
	syntaxOnly := flags.Bool("syntax-only", false, "validate checked-in reports without stable thresholds or Git tags")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: validate-report does not accept positional arguments")
		return 2
	}
	modes := 0
	if *syntaxOnly {
		modes++
	}
	if *releaseTag != "" {
		modes++
	}
	if modes != 1 {
		fmt.Fprintln(os.Stderr, "error: choose exactly one of --syntax-only or --release-tag")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if *syntaxOnly {
		count, err := releasegate.ValidateSyntaxDirectory(*reportsDirectory)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("validated %d acceptance report(s) in syntax-only mode\n", count)
		return 0
	}
	result, err := releasegate.ValidateRelease(ctx, *repository, *releaseTag, *reportsDirectory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if result.RC {
		fmt.Printf("validated RC tag %s; stable acceptance report not required\n", result.ReleaseTag)
		return 0
	}
	fmt.Printf("validated stable tag %s against %s acceptance report\n", result.ReleaseTag, result.RCTag)
	return 0
}
