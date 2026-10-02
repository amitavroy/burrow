package main

import (
	"fmt"
	"io"
	"os"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: syncd <command>")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	default:
		fmt.Fprintf(stderr, "syncd: unknown command %q\n", args[0])
		return 2
	}
}
