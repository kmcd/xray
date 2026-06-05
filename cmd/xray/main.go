package main

import (
	"fmt"
	"os"
)

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("xray %s (commit %s, built %s)\n", version, commit, date)
	case "help", "--help", "-h":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "xray: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: xray <command> [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  version    print version")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "planned (M1+):")
	fmt.Fprintln(w, "  init       generate a starter config from a GitHub org")
	fmt.Fprintln(w, "  validate   syntactic check on a config file")
	fmt.Fprintln(w, "  check      live preflight against configured connectors")
	fmt.Fprintln(w, "  run        full extraction")
}
