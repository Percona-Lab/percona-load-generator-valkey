// Command plgv is the Percona Load Generator for Valkey.
package main

import (
	"fmt"
	"os"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println("plgv", version)
	case "help", "--help", "-h":
		usage(os.Stdout)
	case "run":
		os.Exit(runCmd(args[1:], os.Stdout, os.Stderr))
	case "web":
		os.Exit(webCmd(args[1:], os.Stdout, os.Stderr))
	case "replay":
		os.Exit(replayCmd(args[1:], os.Stdout, os.Stderr))
	case "compare":
		os.Exit(compareCmd(args[1:], os.Stdout, os.Stderr))
	case "import-traffic":
		os.Exit(importCmd(args[1:], os.Stdout, os.Stderr))
	case "presets":
		os.Exit(presetsCmd(os.Stdout))
	case "config":
		os.Exit(configCmd(args[1:], os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "plgv: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "Percona Load Generator for Valkey (plgv)")
	fmt.Fprintln(w, "Usage: plgv <command> [flags]")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  run      run a workload against a standalone Valkey (plgv run -h)")
	fmt.Fprintln(w, "  web      start the web UI (plgv web -h)")
	fmt.Fprintln(w, "  config   validate and print the effective config (plgv config -h)")
	fmt.Fprintln(w, "  replay   send a recorded workload to a server again (plgv replay -h)")
	fmt.Fprintln(w, "  import-traffic  turn captured MONITOR or SLOWLOG traffic into a recording (plgv import-traffic -h)")
	fmt.Fprintln(w, "  compare  compare replay results of the same recording (plgv compare -h)")
	fmt.Fprintln(w, "  presets  list the built-in workload presets")
	fmt.Fprintln(w, "  version  print the version")
	fmt.Fprintln(w, "  help     print this help")
}
