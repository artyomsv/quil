package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// RunSSH executes one command on a remote host and returns its exit status.
//
// It shares sshArgs with SSH(), so every hardening option, the ConnectTimeout
// and the destination check apply identically. That sharing is the point: a
// second ssh call site assembling its own argument vector would silently drop
// the guard against a destination like "-oProxyCommand=...", which executes a
// command on THIS machine before any network traffic happens.
//
// Unlike SSH() this is synchronous and reaps the child itself — callers here
// want a finished command and its status, not a live connection.
//
// A non-zero exit is returned as a status with a nil error: for this caller
// "the remote command failed" is data, not an exception. Only a failure to run
// ssh at all is an error, and the status is then -1.
func RunSSH(ctx context.Context, dest string, opts SSHOptions, command string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {

	// Same rejection as cmd/quil's flag parsing and SSH()'s dial. Duplicated at
	// each entry point on purpose: the failure mode is silent local code
	// execution, and a guard that lives in only one of three call sites is one
	// refactor away from protecting none of them.
	if strings.HasPrefix(dest, "-") {
		return -1, fmt.Errorf("invalid ssh destination %q: must not begin with '-'", dest)
	}
	if command == "" {
		return -1, errors.New("empty remote command")
	}

	sshPath := opts.SSHPath
	if sshPath == "" {
		sshPath = "ssh"
	}
	resolved, err := exec.LookPath(sshPath)
	if err != nil {
		return -1, fmt.Errorf("locate ssh binary %q: %w", sshPath, err)
	}

	opts.RemoteCommand = command
	cmd := exec.CommandContext(ctx, resolved, sshArgs(dest, opts)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("run ssh to %s: %w", dest, err)
	}
	return 0, nil
}

// NewTerminalSanitizer wraps a terminal-bound writer so remote-influenced
// output cannot emit control sequences into it.
//
// Exported because callers outside this package now run ssh commands whose
// stderr reaches the operator's terminal, and that stream carries whatever the
// remote shell and its rc files wrote — verbatim, and attacker-controlled if
// the host is compromised.
func NewTerminalSanitizer(w io.Writer) io.Writer { return &terminalSanitizer{w: w} }
