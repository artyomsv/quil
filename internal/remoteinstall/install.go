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

// maxRemoteOutput caps what any single remote command may return.
//
// The buffers below hold whatever the far side writes, and nothing else bounds
// them: the 10-minute setup timeout is the only other limit, which at ssh
// throughput is gigabytes. A host with an endlessly-writing rc file would
// otherwise exhaust local memory before the consent prompt is even reached,
// since the probe runs first. Generous for a five-line contract.
const maxRemoteOutput = 64 << 10

// capWriter discards everything past a byte limit, reporting full writes so the
// exec copier treats it as a healthy sink rather than a short-write error.
type capWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *capWriter) Write(p []byte) (int, error) {
	// n is captured BEFORE the slice below is narrowed. Returning the truncated
	// length would be a short write, which io.Copy and exec's copier goroutine
	// both treat as an error — turning "the host said too much" into "the
	// command failed".
	n := len(p)
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

func (w *capWriter) String() string { return w.buf.String() }

// RunProbe asks the remote host what it is and whether quil is already there.
func RunProbe(ctx context.Context, r Runner) (Probe, error) {
	stdout := &capWriter{limit: maxRemoteOutput}
	stderr := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, probeCommand, strings.NewReader(probeScript), stdout, stderr)
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

// notRunningMarker is what `quil daemon stop` prints when there was nothing to
// stop. Matched to tell that benign outcome apart from a real failure.
const notRunningMarker = "daemon not running"

// StopRemoteDaemon stops the daemon owned by an existing remote install, so the
// replacement binary is what serves the next attach.
//
// It returns a WARNING string rather than an error, because the exit code alone
// cannot answer the question. `quil daemon stop` exits 1 both when the stop
// genuinely failed and when no daemon was running — and the second is the
// common case and precisely the state we want. Propagating non-zero would abort
// every upgrade of an idle host; swallowing it hides a daemon that refused to
// die, which then keeps serving the OLD binary (renaming over a running
// executable leaves the running process on its original inode), so the next
// attach reports a version mismatch the user has already "fixed".
//
// So: classify on the marker our own CLI prints, and treat everything else as
// worth surfacing. A remote running an OLDER quil may word it differently,
// which costs a spurious warning — never a silent failure.
func StopRemoteDaemon(ctx context.Context, r Runner, binaryPath string) (warning string, err error) {
	out := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, DaemonStopCommand(binaryPath), nil, out, out)
	if err != nil {
		return "", fmt.Errorf("stop remote daemon: %w", err)
	}
	if code == 0 || strings.Contains(out.String(), notRunningMarker) {
		return "", nil
	}
	detail := firstLine(out.String())
	if detail == "" {
		detail = fmt.Sprintf("exited %d with no output", code)
	}
	return detail, nil
}

// Push streams the archive into the remote install script.
func Push(ctx context.Context, r Runner, t Target, src Source) error {
	stdout := &capWriter{limit: maxRemoteOutput}
	stderr := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, InstallCommand(t, src), bytes.NewReader(src.Archive), stdout, stderr)
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
