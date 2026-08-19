//go:build integration

package daemon

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Issue #169: a claude-code pane cleared its own conversation after a daemon
// restart, on every AI pane at once, with nobody at the keyboard.
//
// The sequence below is the ordinary first attach after a restart:
//
//  1. The pane came off disk with a GhostSnap and its plugin resumes its own
//     session (preassign_id), so handleAttach skips the replay and kicks.
//  2. The attaching client's first resize_pane follows. A restored pane has
//     appliedCols/appliedRows == 0, so the same-size guard cannot suppress it
//     and repaintAfterResize kicks again.
//
// Measured before the fix: two form feeds 5.7 ms apart. claude-code >= v2.1.126
// runs /clear on two Ctrl+L within two seconds, so the pair discarded the
// conversation. This asserts the pane receives exactly ONE inside that window.
func TestIssue169_AttachThenFirstResize_DeliversOneRedrawKey(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	pty := &stampedSession{}
	pane.PluginMu.Lock()
	pane.Type = "claude-code"
	pane.PTY = pty
	// GhostSnap non-nil is what "first attach after a restore" means.
	pane.GhostSnap = bytes.Repeat([]byte{'g'}, 4096)
	pane.PluginMu.Unlock()
	t.Cleanup(pane.StopInput)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Guards: the scenario only exists for a plugin that both declares a redraw
	// key and restores its own history. If either stops holding, fail loudly
	// rather than pass vacuously.
	p := d.registry.Get("claude-code")
	if p == nil || p.Persistence.RedrawKey != "\f" {
		t.Fatal("setup: claude-code must declare redraw_key = \"\\f\"")
	}
	if !restoresOwnHistory(p) {
		t.Fatal("setup: claude-code must resolve to a session-resuming strategy")
	}
	// The real cooldown, not a shrunk one: this test is about the shipped value.
	if redrawKeyCooldown <= 2*time.Second {
		t.Fatalf("redrawKeyCooldown = %v, too short to cover claude's window", redrawKeyCooldown)
	}

	conn := dialDaemon(t, filepath.Join(tmp, "quild.sock"))
	defer conn.Close()

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage attach: %v", err)
	}
	if err := ipc.WriteMessage(conn, attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}
	if !waitFor(t, func() bool { return pty.count() >= 1 }) {
		t.Fatalf("attach delivered %q, want one form feed", pty.got())
	}

	// The attaching client's first resize. 80x24 matches the attach payload —
	// the point is that the pane has never had a size APPLIED, so this is a
	// genuine resize however ordinary the numbers are.
	if err := ipc.WriteMessage(conn, resizeMsg(t, pane.ID, 80, 24)); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	// Well inside claude's two-second window, and long enough that an
	// unthrottled second kick — 5.7 ms behind the first, measured — has landed.
	time.Sleep(time.Second)
	if got := pty.got(); got != "\f" {
		t.Fatalf("child received %q within claude's /clear window, want a single "+
			"form feed — two is what claude-code reads as /clear (issue #169)", got)
	}
}

// stampedSession records stdin writes with arrival times, so a failure can
// report the interval rather than only the bytes.
type stampedSession struct {
	fakeSession
	mu      sync.Mutex
	written []byte
	at      []time.Time
}

func (s *stampedSession) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, data...)
	for range data {
		s.at = append(s.at, time.Now())
	}
	return len(data), nil
}

func (s *stampedSession) got() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.written)
}

func (s *stampedSession) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.written)
}

func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
