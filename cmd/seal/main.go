package main

import (
	"fmt"
	"os"
)

const version = "0.0.0-dev"

const help = `Seal provides evidence-backed completion for coding agents.

Usage:
  seal --help
  seal --version

Options:
  --help     Show this help text.
  --version  Print the development version.

This experimental Go successor does not yet implement Task, Run, Evidence,
Verdict, or completion operations.
`

func main() {
	args := os.Args[1:]

	if len(args) == 1 && args[0] == "--help" {
		fmt.Print(help)
		return
	}

	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(version)
		return
	}

	fmt.Fprintln(os.Stderr, "seal: expected exactly one of --help or --version")
	os.Exit(2)
}
