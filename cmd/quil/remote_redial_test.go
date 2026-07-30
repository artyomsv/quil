package main

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
)

// stubDial installs a fake dial for one test and restores the real one after.
//
// Every reconnect assertion in this file needs the dial to be observable and to
// finish instantly; spawning a real ssh would make them slow, environment-
// dependent, and unable to fabricate the link signals the classifier reads.
func stubDial(t *testing.T, fn func(context.Context, config.Config, bool, io.Writer) (*ipc.Client, transport.LinkStatus, error)) {
	t.Helper()
	prev := dialRemoteTransportFn
	dialRemoteTransportFn = fn
	t.Cleanup(func() { dialRemoteTransportFn = prev })
}

// TestRedialRemote_DialsBatchWithTheLogSink pins the two dial options a
// reconnect cannot get wrong.
//
// Batch, because Bubble Tea owns the terminal in raw mode by reconnect time: a
// non-batch attempt that decides to ask for a host key or a passphrase has
// nowhere to read the answer from and stalls until the timeout kills it, with
// the prompt itself painted over the TUI.
//
// The sink, because RedirectStderr — the seam the STARTUP dial uses — is a no-op
// on a batch dial: stderr is captured into a buffer that never reaches a
// terminal, so there is nothing to redirect. Without the sink passed here, ssh's
// diagnostics are visible for the first connection and silent for every
// reconnect after it, which is exactly when they are wanted (RD-018).
func TestRedialRemote_DialsBatchWithTheLogSink(t *testing.T) {
	withRemote(t, "gpu01")

	var gotBatch bool
	var gotSink io.Writer
	called := false
	stubDial(t, func(_ context.Context, _ config.Config, batch bool, sink io.Writer) (*ipc.Client, transport.LinkStatus, error) {
		called, gotBatch, gotSink = true, batch, sink
		return nil, nil, errors.New("stub dial")
	})

	if _, err := redialRemote(config.Config{})(nil); err == nil {
		t.Fatal("redial reported success on a failed dial")
	}
	if !called {
		t.Fatal("redialRemote did not dial through dialRemoteTransportFn; the assertions below prove nothing")
	}
	if !gotBatch {
		t.Error("reconnect dialled in interactive mode; ssh may prompt with Bubble Tea holding the terminal")
	}
	if gotSink == nil {
		t.Fatal("reconnect dialled with no stderr sink; ssh diagnostics vanish after the first reconnect")
	}
	// Specifically the framer, not the log writer itself. terminalSanitizer
	// preserves \n, so raw bytes appended to the log land at column 0 and a
	// remote can emit a line that reads exactly like a genuine slog record.
	logger, ok := gotSink.(*sshStderrLogger)
	if !ok {
		t.Fatalf("stderr sink is %T, want *sshStderrLogger so remote text is quoted per line", gotSink)
	}
	if logger.dest != "gpu01" {
		t.Errorf("sink dest = %q, want %q", logger.dest, "gpu01")
	}
}

// TestRedialRemote_ReusesOneStderrSinkAcrossAttempts pins the byte budget
// against the link that makes it necessary.
//
// sshStderrBudget caps what one session may write to the log, because the sink
// is remote-influenced and the rotating writer keeps only max_files archives —
// an unbounded remote rolls the whole set and evicts every other record of what
// happened. A per-attempt sink resets that counter on every reconnect, so a
// FLAPPING link — the one case where the budget matters most, and the one this
// whole subsystem exists to survive — would restore unbounded writing.
//
// Nothing else catches it: the budget's own test drives a single sink directly,
// and moving the construction inside the closure keeps every other test green.
func TestRedialRemote_ReusesOneStderrSinkAcrossAttempts(t *testing.T) {
	withRemote(t, "gpu01")

	var sinks []io.Writer
	stubDial(t, func(_ context.Context, _ config.Config, _ bool, sink io.Writer) (*ipc.Client, transport.LinkStatus, error) {
		sinks = append(sinks, sink)
		return nil, nil, errors.New("stub dial")
	})

	redial := redialRemote(config.Config{})
	for i := 0; i < 3; i++ {
		if _, err := redial(nil); err == nil {
			t.Fatalf("attempt %d reported success on a failed dial", i+1)
		}
	}

	if len(sinks) != 3 {
		t.Fatalf("dialled %d times, want 3", len(sinks))
	}
	for i, s := range sinks[1:] {
		if s != sinks[0] {
			t.Errorf("attempt %d got a fresh stderr sink (%p, want %p); the session byte "+
				"budget resets on every reconnect", i+2, s, sinks[0])
		}
	}
}

// recordingLink notes the order its signals are read in, so a test can assert
// what happens on either side of Close.
type recordingLink struct {
	fakeLink
	order *[]string
}

func (l recordingLink) LinkErr() error {
	*l.order = append(*l.order, "linkerr")
	return l.fakeLink.LinkErr()
}

func (l recordingLink) ExitCode() int {
	*l.order = append(*l.order, "exitcode")
	return l.fakeLink.ExitCode()
}

// TestRedialRemote_ReadsLinkErrBeforeCloseAndExitCodeAfter pins the ordering the
// verify-failure branch depends on, in the loop rather than in the version gate.
//
// The two reads bracket Close and neither can move:
//
//   - LinkErr BEFORE. Close unblocks the transport's pump, and that return path
//     can complete without ever setting pumpErr — so a later read comes back nil
//     and the banner falls back to "await version response: i/o timeout", naming
//     nothing the user can act on.
//   - ExitCode AFTER. Close is what reaps the ssh child, so the status is only
//     final then. Read early it is -1, which fails the classifier's 255 gate and
//     turns every permanent failure back into an unbounded retry (RD-019).
//
// Close itself is in the trace on purpose. Recording only the two seams would
// pass unchanged if a refactor hoisted the ExitCode read above Close — the exact
// regression this guards. Same reasoning as the version gate's own ordering test.
func TestRedialRemote_ReadsLinkErrBeforeCloseAndExitCodeAfter(t *testing.T) {
	withRemote(t, "gpu01")

	var order []string
	// A client over an already-closed pipe: the version probe fails on its first
	// write, so the verify branch is reached without a fake daemon.
	client := clientRecordingClose(t, &order)
	link := recordingLink{
		fakeLink: fakeLink{
			err:         errors.New("ssh: Permission denied (publickey)."),
			established: false,
			exitCode:    transport.ExitSSHOwnFailure,
		},
		order: &order,
	}
	stubDial(t, func(context.Context, config.Config, bool, io.Writer) (*ipc.Client, transport.LinkStatus, error) {
		return client, link, nil
	})

	if _, err := redialRemote(config.Config{})(nil); err == nil {
		t.Fatal("redial reported success against a dead connection")
	}

	want := []string{"linkerr", "close", "exitcode"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// TestRedialRemote_ClassifiesVerifyFailures joins the classifier to the loop
// end-to-end.
//
// markPermanentLinkFailure is covered directly and the TUI's reaction to a
// wrapped sentinel is covered in internal/tui, but neither proves redialRemote
// calls it on the path where auth failures actually arrive. That path is the
// VERIFY branch, never the dial branch: exec.Cmd.Start succeeds as soon as the
// ssh binary launches, so a dial cannot fail for network or auth reasons and
// `link` is always nil there. Classifying in the wrong branch compiles, passes
// every unit test, and never parks anything.
func TestRedialRemote_ClassifiesVerifyFailures(t *testing.T) {
	denied := errors.New("ssh: Permission denied (publickey).")
	timedOut := errors.New("ssh: connect to host gpu01 port 22: Connection timed out")

	tests := []struct {
		name      string
		link      fakeLink
		permanent bool
	}{
		{
			name:      "ssh denied before the remote ever ran",
			link:      fakeLink{err: denied, established: false, exitCode: transport.ExitSSHOwnFailure},
			permanent: true,
		},
		{
			// ssh multiplexes the REMOTE command's fd 2 onto its own stderr, so
			// these words can be an ordinary ~/.bashrc touching an unreadable
			// path. Any byte read proves the remote command ran, which proves ssh
			// authenticated, which proves the marker is not ssh's own.
			name:      "the same words once the remote has spoken are the remote's",
			link:      fakeLink{err: denied, established: true, exitCode: transport.ExitSSHOwnFailure},
			permanent: false,
		},
		{
			// ssh passes the remote command's status through untouched, so a
			// non-255 status means the failure is not ssh's own.
			name:      "the same words with the remote command's own status",
			link:      fakeLink{err: denied, established: false, exitCode: 127},
			permanent: false,
		},
		{
			// Unmatched means transient, deliberately: mis-parking a session that
			// would have healed is the worse error.
			name:      "a transient failure keeps retrying",
			link:      fakeLink{err: timedOut, established: false, exitCode: transport.ExitSSHOwnFailure},
			permanent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withRemote(t, "gpu01")
			var order []string
			client := clientRecordingClose(t, &order)
			stubDial(t, func(context.Context, config.Config, bool, io.Writer) (*ipc.Client, transport.LinkStatus, error) {
				return client, tt.link, nil
			})

			_, err := redialRemote(config.Config{})(nil)
			if err == nil {
				t.Fatal("redial reported success against a dead connection")
			}
			if got := errors.Is(err, tui.ErrLinkPermanent); got != tt.permanent {
				t.Errorf("errors.Is(_, ErrLinkPermanent) = %v, want %v (err: %v)", got, tt.permanent, err)
			}
			// ssh's own words must survive either way — the banner renders this,
			// and the generic probe error names nothing actionable. Matched on
			// text, not errors.Is: the wrap is "%v: %w" with the SENTINEL in the
			// %w slot, so the cause is deliberately not unwrappable.
			if !strings.Contains(err.Error(), tt.link.err.Error()) {
				t.Errorf("error %q dropped the link cause %q", err, tt.link.err)
			}
		})
	}
}
