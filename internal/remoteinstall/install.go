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

// exitSSHOwnFailure is ssh's status for a failure of its own (auth, host key,
// connection). The remote command's status passes through untouched.
const exitSSHOwnFailure = 255

// RunProbe asks the remote host what it is and whether quil is already there.
//
// A Windows host has no `sh`: both cmd and PowerShell exit 1 for a missing
// command, and a host running Git for Windows' `sh` answers but reports
// `uname -s` as MINGW64_NT-… rather than Linux or Darwin. Either shape
// switches to the PowerShell probe (runWindowsProbe) rather than failing —
// ssh's OWN failure (255) is excluded, since retrying with a second command
// after ssh itself could not connect would just fail the same way again.
func RunProbe(ctx context.Context, r Runner) (Probe, error) {
	stdout := &capWriter{limit: maxRemoteOutput}
	stderr := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, probeCommand, strings.NewReader(probeScript), stdout, stderr)
	if err != nil {
		return Probe{}, fmt.Errorf("run remote probe: %w", err)
	}
	switch {
	case code == 0:
		// The probe script always exits 0, deliberately, so success here means
		// the shell ran it. A Git-for-Windows `sh` also exits 0, which is why
		// this branch still has to check the ANSWER, not just the code.
		p, perr := ParseProbe(stdout.String())
		if perr == nil {
			return p, nil
		}
		if !unameLooksWindows(stdout.String()) {
			return Probe{}, perr
		}
		return runWindowsProbe(ctx, r)
	case code != exitSSHOwnFailure && !strings.Contains(stdout.String(), probeSentinel):
		// No `sh` on the far side: cmd and PowerShell both exit 1 for a
		// missing command. A Windows host answers the second probe.
		return runWindowsProbe(ctx, r)
	}
	return Probe{}, fmt.Errorf("ssh exited %d before the probe could run: %s", code, firstLine(stderr.String()))
}

// runWindowsProbe sends remote-probe.ps1, base64-encoded, to a host that
// answered the POSIX probe with neither a parseable report nor an
// sh-not-found status.
func runWindowsProbe(ctx context.Context, r Runner) (Probe, error) {
	stdout := &capWriter{limit: maxRemoteOutput}
	stderr := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, EncodePowerShell(windowsProbeScript), nil, stdout, stderr)
	if err != nil {
		return Probe{}, fmt.Errorf("run Windows probe: %w", err)
	}
	if code != 0 {
		return Probe{}, fmt.Errorf("the host has neither sh nor PowerShell (probe exited %d): %s",
			code, firstLine(stderr.String()))
	}
	return ParseWindowsProbe(stdout.String())
}

// unameLooksWindows reports a POSIX probe answered by Git for Windows, MSYS2
// or Cygwin: uname -s (the 2nd line after the last sentinel) starts MINGW,
// MSYS or CYGWIN.
func unameLooksWindows(out string) bool {
	lines := strings.Split(out, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(strings.TrimRight(l, "\r")) == probeSentinel {
			start = i + 1
		}
	}
	if start < 0 || len(lines) <= start+1 {
		return false
	}
	u := strings.ToUpper(strings.TrimSpace(lines[start+1]))
	return strings.HasPrefix(u, "MINGW") || strings.HasPrefix(u, "MSYS") || strings.HasPrefix(u, "CYGWIN")
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
func StopRemoteDaemon(ctx context.Context, r Runner, shell, binaryPath string) (warning string, err error) {
	cmd, err := DaemonStopCommand(shell, binaryPath)
	if err != nil {
		return "", err
	}
	out := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, cmd, nil, out, out)
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

// Staged is an archive that has reached the host and been unpacked into a
// staging directory inside the install directory, but not installed.
//
// Opaque on purpose: the only thing a caller can do with one is hand it to
// Commit, so the staging path the host reported cannot be used for anything
// else.
type Staged struct {
	target  Target
	src     Source // the Windows finalize step re-verifies every file's hash
	staging string
}

// Stage sends the archive to the host and unpacks it next to the install,
// changing nothing the host runs. shell is the host's default ssh shell
// (Probe.Shell); only the Windows steps quote a command for it, since a POSIX
// host always runs the scripts under `sh -c`.
//
// The install is split in two because the remote daemon must be stopped in
// between, and the order used to be stop, then write. Every likely failure is
// in the WRITE — a full disk or quota, a dropped link, a corrupted transfer —
// so a failed upgrade stopped the daemon, killed what its panes were running,
// and then installed nothing. Measured: a host whose /tmp hit its per-user
// quota had its daemon stopped three times by three retries, each ending in
// "Disk quota exceeded". Staging first moves all of those failures to before
// the stop, where they cost nothing.
func Stage(ctx context.Context, r Runner, shell string, t Target, src Source) (Staged, error) {
	if t.OS == "windows" {
		return stageWindows(ctx, r, shell, t, src)
	}
	out, err := runStep(ctx, r, StageCommand(t, src), bytes.NewReader(src.Archive), "copy the archive into "+t.Dir)
	if err != nil {
		return Staged{}, err
	}
	staging, err := parseStageOutput(t, out)
	if err != nil {
		return Staged{}, fmt.Errorf("copy the archive into %s: %w", t.Dir, err)
	}
	return Staged{target: t, src: src, staging: staging}, nil
}

// Commit moves what Stage unpacked into place. Each binary is a rename within
// one filesystem, so this is the step least able to fail — which is why it is
// the one that runs after the daemon is stopped.
func Commit(ctx context.Context, r Runner, s Staged) error {
	if s.staging == "" {
		return fmt.Errorf("nothing was staged for %s", s.target.Dir)
	}
	if s.target.OS == "windows" {
		finalize, err := WindowsFinalizeCommand(s.target, s.staging, s.src)
		if err != nil {
			return err
		}
		_, err = runStep(ctx, r, finalize, nil, "install into "+s.target.Dir)
		return err
	}
	_, err := runStep(ctx, r, CommitCommand(s.target, s.staging), nil, "install into "+s.target.Dir)
	return err
}

// stageSentinel precedes the staging path in remote-stage.sh's output.
const stageSentinel = "__quil_staged__"

// posixStagingPrefix is the staging dir's name before mktemp's random suffix.
// The same prefix the Windows install uses, so one name means one thing.
const posixStagingPrefix = ".quil-staging-"

// parseStageOutput returns the staging directory remote-stage.sh reported.
//
// The path is remote-reported and becomes an argument to a command that ends
// in `rm -rf`, so it is checked rather than trusted: exactly t.Dir + "/" +
// ".quil-staging-" + mktemp's alphanumeric suffix. The LAST sentinel wins, as
// in the probe, so text an rc file prints first cannot shift the answer, and a
// forged block printed before ours is overridden by ours.
func parseStageOutput(t Target, out string) (string, error) {
	lines := strings.Split(out, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == stageSentinel {
			start = i + 1
		}
	}
	if start < 0 || start >= len(lines) {
		return "", fmt.Errorf("the host did not report a staging directory")
	}
	staging := strings.TrimRight(lines[start], "\r")
	prefix := t.Dir + "/" + posixStagingPrefix
	suffix, ok := strings.CutPrefix(staging, prefix)
	if !ok || len(suffix) < 6 || !isAlnum(suffix) {
		return "", fmt.Errorf("staging directory %q is not %s<random>", firstLine(staging), prefix)
	}
	return staging, nil
}

func isAlnum(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// stageWindows runs the first two of the three steps wincommand.go describes:
// prepare, then extract with the host's tar.exe. finalize is Commit's.
func stageWindows(ctx context.Context, r Runner, shell string, t Target, src Source) (Staged, error) {
	prepare, err := WindowsPrepareCommand(t)
	if err != nil {
		return Staged{}, err
	}
	// Build the finalize command's hash table BEFORE anything runs, so a
	// Source it would refuse cannot leave a staging dir behind on the host.
	if _, err := hashTable(src.FileSHA256); err != nil {
		return Staged{}, err
	}

	out, err := runStep(ctx, r, prepare, nil, "prepare "+t.Dir)
	if err != nil {
		return Staged{}, err
	}
	staging, tar, err := ParsePrepareOutput(t, out)
	if err != nil {
		return Staged{}, fmt.Errorf("prepare %s: %w", t.Dir, err)
	}

	extract, err := WindowsExtractCommand(shell, tar, staging)
	if err != nil {
		return Staged{}, err
	}
	if _, err := runStep(ctx, r, extract, bytes.NewReader(src.Archive), "copy the archive into "+staging); err != nil {
		return Staged{}, err
	}
	return Staged{target: t, src: src, staging: staging}, nil
}

// runStep runs one remote command and turns a non-zero exit into
// "<what> failed: <first line of stderr, else stdout>". It returns stdout.
func runStep(ctx context.Context, r Runner, cmd string, stdin io.Reader, what string) (string, error) {
	stdout := &capWriter{limit: maxRemoteOutput}
	stderr := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, cmd, stdin, stdout, stderr)
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	if code != 0 {
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = firstLine(stdout.String())
		}
		if detail == "" {
			detail = fmt.Sprintf("exited %d", code)
		}
		return "", fmt.Errorf("%s failed: %s", what, detail)
	}
	return stdout.String(), nil
}

// InstallLogonTask registers the daemon's logon task on a Windows remote. Like
// StopRemoteDaemon it returns a WARNING, not an error, for a non-zero exit:
// the install already succeeded, and the task is an improvement the user can
// add by hand.
func InstallLogonTask(ctx context.Context, r Runner, shell, binaryPath string) (string, error) {
	cmd, err := QuoteCommand(shell, binaryPath, "daemon", "install-logon")
	if err != nil {
		return "", err
	}
	out := &capWriter{limit: maxRemoteOutput}
	code, err := r.Run(ctx, cmd, nil, out, out)
	if err != nil {
		return "", fmt.Errorf("register the logon task: %w", err)
	}
	if code == 0 {
		return "", nil
	}
	if d := firstLine(out.String()); d != "" {
		return d, nil
	}
	return fmt.Sprintf("exited %d with no output", code), nil
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
