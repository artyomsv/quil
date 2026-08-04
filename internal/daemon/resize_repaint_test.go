package daemon

import (
	"errors"
	"testing"
	"time"
)

var errSimulatedResize = errors.New("simulated resize failure")

// A restored pane is spawned at its persisted size BEFORE any client attaches,
// then the first client's resize moves it. claude-code re-lays-out on that
// resize but paints only on its own render tick — which input drives — so the
// paint it had already made stays on screen at the OLD width while everything
// drawn afterwards lands at the new one. That is not a momentary flicker: the
// stale rows persist underneath, which is what produced the overlapping banner
// (a status line and a working-directory line rendered through each other)
// reported on 2026-08-02 after the project sidebar started reserving 22
// columns and made the spawn-vs-attach size disagreement happen every launch.
//
// A declared redraw_key already MEANS "this program ignores SIGWINCH" — that is
// the contract redrawKick relies on — so the same key is what makes it repaint
// after a resize.

// waitForNoInput is the inverse of waitForInput: it gives the input writer a
// real chance to deliver and asserts nothing arrived. A bare read would pass
// simply by running before the goroutine did.
func waitForNoInput(t *testing.T, s *recordingSession) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := s.got(); got != "" {
			t.Fatalf("child received %q, want nothing written to stdin", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandleResizePane_RedrawsAPluginThatIgnoresSIGWINCH(t *testing.T) {
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	d.session.panes["p1"] = pane
	t.Cleanup(pane.StopInput)

	// The spawn-vs-attach disagreement: the pane came up at the persisted 91
	// columns and the first client resize narrows it.
	pane.appliedCols, pane.appliedRows = 91, 54
	d.handleResizePane(resizeMsg(t, "p1", 80, 52))

	if !waitForInput(t, pty, "\f") {
		t.Errorf("child received %q after a resize, want %q (Ctrl+L)", pty.got(), "\f")
	}
}

// The opt-in property, and the reason this reuses redraw_key rather than
// sending some key of its own: a plain terminal may be sitting at a password
// prompt or in `cat > file`, where an injected form feed is data, not a
// repaint. It also repaints on SIGWINCH, which the resize already delivered.
func TestHandleResizePane_NoInjectedInputWithoutAnOptIn(t *testing.T) {
	d := daemonWithPlugin(t, "terminal", "", true)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "terminal", PTY: pty}
	d.session.panes["p1"] = pane
	t.Cleanup(pane.StopInput)

	d.handleResizePane(resizeMsg(t, "p1", 80, 52))

	waitForNoInput(t, pty)
}

// The TUI re-sends every pane's size on every workspace broadcast, so the
// redraw MUST sit behind the same-size guard. Ahead of it, an idle session
// would type Ctrl+L into every AI pane on each broadcast.
func TestHandleResizePane_DuplicateSizeSendsNoRedraw(t *testing.T) {
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	d.session.panes["p1"] = pane
	t.Cleanup(pane.StopInput)

	// Already applied — this is the broadcast re-send, not a real resize.
	pane.appliedCols, pane.appliedRows = 80, 52
	d.handleResizePane(resizeMsg(t, "p1", 80, 52))

	waitForNoInput(t, pty)
}

// A resize that FAILED left the child at its old size, so telling it to
// repaint would only redraw the stale geometry. The guard is left untouched on
// failure so the next broadcast retries; the redraw has to follow it.
func TestHandleResizePane_FailedResizeSendsNoRedraw(t *testing.T) {
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &failingRecordingSession{fail: true}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	d.session.panes["p1"] = pane
	t.Cleanup(pane.StopInput)

	d.handleResizePane(resizeMsg(t, "p1", 80, 52))

	waitForNoInput(t, &pty.recordingSession)
}

// failingRecordingSession records stdin like recordingSession but fails Resize.
type failingRecordingSession struct {
	recordingSession
	fail bool
}

func (f *failingRecordingSession) Resize(rows, cols uint16) error {
	if f.fail {
		return errSimulatedResize
	}
	return nil
}
