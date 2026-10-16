package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sbx"
)

// runSandboxTrust approves a project's sbx sandbox profile.
//
// The profile lives in the repository, which is what an sbx agent can write,
// so the daemon refuses to use one until the user has looked at it: this
// command lists every file the approval covers — the profile, the environment
// file, its local kits and the scripts — and records their digest. Any later
// change to any of them makes the daemon refuse again until this is re-run.
func runSandboxTrust(args []string) {
	if remoteMode() || connectMode() {
		fmt.Fprintln(os.Stderr, "quil sandbox trust: not available with --remote or --connect.\n"+
			"The approval is stored beside the daemon that runs the sandbox, so run this on that host.")
		os.Exit(1)
	}
	dir, yes, err := parseTrustArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "quil sandbox trust:", err)
		fmt.Fprintln(os.Stderr, "usage: quil sandbox trust [dir] [--yes]")
		os.Exit(1)
	}
	if err := sandboxTrust(dir, yes, os.Stdin, os.Stdout, config.QuilDir()); err != nil {
		fmt.Fprintln(os.Stderr, "quil sandbox trust:", err)
		os.Exit(1)
	}
}

func parseTrustArgs(args []string) (dir string, yes bool, err error) {
	dir = "."
	seenDir := false
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown flag %s", a)
		case seenDir:
			return "", false, fmt.Errorf("more than one directory")
		default:
			dir, seenDir = a, true
		}
	}
	return dir, yes, nil
}

// sandboxTrust is the testable body: find, load, list, confirm, record.
func sandboxTrust(dir string, yes bool, in io.Reader, out io.Writer, quilDir string) error {
	// The daemon's own lookup, so a worktree approves its main checkout's
	// profile — the one its panes are checked against.
	path, err := sbx.FindProfileFor(dir)
	if err != nil {
		return err
	}
	prof, err := sbx.LoadProfile(path)
	if err != nil {
		return err
	}
	files, err := sbx.PinnedFiles(prof)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Profile:  %s\nSandbox:  %s\nEnv file: %s\n", prof.Path, prof.Name, prof.EnvFile)
	if len(prof.PrepareHost) > 0 {
		fmt.Fprintf(out, "Runs ON THIS HOST before a create: %s\n", strings.Join(prof.PrepareHost, " "))
	}
	for i, step := range prof.PostCreate {
		fmt.Fprintf(out, "Runs inside the sandbox after a create (%d): %s\n", i+1, strings.Join(step, " "))
	}
	fmt.Fprintln(out, "\nThe approval covers these files; changing any of them needs a new approval:")
	for _, f := range files {
		fmt.Fprintln(out, "  "+f)
	}
	if !yes {
		fmt.Fprint(out, "\nApprove this profile? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return fmt.Errorf("not approved")
		}
	}
	if _, err := sbx.Trust(sbx.TrustStorePath(quilDir), prof); err != nil {
		return err
	}
	fmt.Fprintln(out, "Approved. New sbx panes in this project can now start.")
	return nil
}
