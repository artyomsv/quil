package remoteinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

// Runner executes one command on the remote host and reports its exit status.
//
// An interface rather than a direct dependency on internal/transport so the
// orchestration is testable without ssh, a network, or a second machine —
// every step below is exercised against a fake.
type Runner interface {
	Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

// RunProbe asks the remote host what it is and whether quil is already there.
func RunProbe(ctx context.Context, r Runner) (Probe, error) {
	var stdout, stderr bytes.Buffer
	code, err := r.Run(ctx, probeCommand, strings.NewReader(probeScript), &stdout, &stderr)
	if err != nil {
		return Probe{}, fmt.Errorf("run remote probe: %w", err)
	}
	// The probe script always exits 0, deliberately, so a non-zero status here
	// came from ssh rather than from anything the probe found. Reporting it as
	// a probe failure would send the user looking at the wrong machine.
	if code != 0 {
		return Probe{}, fmt.Errorf("ssh exited %d before the probe could run: %s",
			code, firstLine(stderr.String()))
	}
	p, err := ParseProbe(stdout.String())
	if err != nil {
		return Probe{}, err
	}
	return p, nil
}

// StopRemoteDaemon stops the daemon owned by an existing remote install, so its
// binary can be replaced underneath it.
//
// A non-zero exit is not an error: the overwhelmingly common case is a daemon
// that was not running, which is exactly the state we want and which `quil
// daemon stop` reports as a failure.
func StopRemoteDaemon(ctx context.Context, r Runner, binaryPath string) error {
	var out bytes.Buffer
	if _, err := r.Run(ctx, DaemonStopCommand(binaryPath), nil, &out, &out); err != nil {
		return fmt.Errorf("stop remote daemon: %w", err)
	}
	return nil
}

// Push streams the archive into the remote install script.
func Push(ctx context.Context, r Runner, t Target, src Source) error {
	var stdout, stderr bytes.Buffer
	code, err := r.Run(ctx, InstallCommand(t, src), bytes.NewReader(src.Archive), &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("run remote installer: %w", err)
	}
	if code != 0 {
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = firstLine(stdout.String())
		}
		if detail == "" {
			detail = fmt.Sprintf("installer exited %d with no output", code)
		}
		return fmt.Errorf("install into %s failed: %s", t.Dir, detail)
	}
	return nil
}

// firstLine trims remote output down to something an error message can carry.
//
// The text comes from the far side, so it is both untrusted and potentially
// unbounded: a chatty rc file or a hostile host could otherwise push arbitrary
// bytes — including terminal escape sequences — into a message printed on the
// operator's terminal.
func firstLine(s string) string {
	s = sanitizeForMessage(s)
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > maxRemoteMessage {
				return line[:maxRemoteMessage] + "…"
			}
			return line
		}
	}
	return ""
}

// maxRemoteMessage bounds how much remote-controlled text one error line may
// carry.
const maxRemoteMessage = 400

// sanitizeForMessage drops the control characters a terminal would act on,
// keeping newline so firstLine can still split. Mirrors the transport package's
// treatment of ssh stderr, which is the same threat with the same shape.
func sanitizeForMessage(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r < 0x20, r == 0x7f: // C0 and DEL
			return -1
		case r >= 0x80 && r <= 0x9f: // C1, including the 0x9b CSI introducer
			return -1
		default:
			return r
		}
	}, s)
}
