// Command sigil is the CLI entrypoint for packaging, serving and detecting.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `sigil — per-viewer forensic watermarking and signed access for HLS

usage: sigil <command> [flags]

commands:
  version   print the version and exit
`

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sigil:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
