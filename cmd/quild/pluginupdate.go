package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/artyomsv/quil/internal/toolupdate"
)

// runPluginUpdate handles the hidden `quild plugin-update` subcommand: the
// daemon runs it as a pane's child to replace a plugin's binary (the Stripe
// CLI first) with a release fetched by tag, so what it prints is what the
// user watches. Every decision was the daemon's and arrives as a flag; this
// loads no plugins, no config and never reads QUIL_HOME, which is why it is
// dispatched before main()'s dev-mode env blocks and works identically in
// every build variant. Returns the process exit code: 0 on success, 1 with
// the error printed as one "✗ " line otherwise.
func runPluginUpdate(args []string) int {
	fs := flag.NewFlagSet("plugin-update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var o toolupdate.Options
	fs.StringVar(&o.Target, "target", "", "path of the binary to replace")
	fs.StringVar(&o.Repo, "repo", "", "GitHub repository, owner/name")
	fs.StringVar(&o.Tag, "tag", "", "release tag to install, e.g. v1.55.1 (no latest fallback)")
	fs.StringVar(&o.Asset, "asset", "", "archive asset name in the release")
	fs.StringVar(&o.Checksums, "checksums", "", "checksums file asset name in the release")
	fs.StringVar(&o.Binary, "binary", "", "file name inside the archive (.exe appended on Windows)")
	fs.StringVar(&o.DetectCmd, "detect", "", "optional version command, e.g. \"stripe --version\"; its first word is replaced by --target")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	if fs.NArg() > 0 {
		// Everything is a named flag; a stray word is a malformed argv from
		// the daemon or a hand-typed mistake, never something to guess at.
		fmt.Fprintf(os.Stderr, "✗ plugin-update: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 1
	}
	o.GOOS = runtime.GOOS

	// Ctrl+C in the pane, a pane close (SIGHUP) or a daemon-side kill
	// (SIGTERM) cancels the download; a swap already under way finishes or
	// rolls back on its own. All three constants exist on windows too, where
	// Go maps the console close/shutdown events onto SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	if err := toolupdate.Run(ctx, o, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	return 0
}
