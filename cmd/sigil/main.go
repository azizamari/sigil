// Command sigil is the CLI entrypoint for packaging, serving and detecting.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

const usage = `sigil — per-viewer forensic watermarking and signed access for HLS

usage: sigil <command> [flags]

commands:
  serve     issue sessions and personalised playlists
  version   print the version and exit

environment:
  SIGIL_API_KEY       shared secret for server-to-server endpoints
  SIGIL_SESSION_KEY   base64 32-byte key sealing playlist tokens
`

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sigil:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stdout)
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
