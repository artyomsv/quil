package daemon

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

// The TUI re-sends every pane's size on each workspace broadcast
// (resizeAllPanes). Re-applying an unchanged size to ConPTY is pure churn;
// handleResizePane must skip it. The guard compares against the size last
// applied to the CURRENT PTY — spawnPane zeroes it on PTY install so a
// fresh PTY always receives its first resize.

func resizeMsg(t *testing.T, paneID string, cols, rows uint16) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
		PaneID: paneID, Cols: cols, Rows: rows,
	})
	if err != nil {
		t.Fatalf("build resize msg: %v", err)
	}
	return msg
}

func TestHandleResizePane_DuplicateSize_SkipsPTYResize(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &fakeSession{}
	pane := &Pane{ID: "p1", PTY: fake}
	d.session.panes["p1"] = pane

	d.handleResizePane(nil, resizeMsg(t, "p1", 100, 40))
	d.handleResizePane(nil, resizeMsg(t, "p1", 100, 40))
	if len(fake.resizes) != 1 {
		t.Fatalf("PTY.Resize called %d times, want 1 (duplicate must be skipped)", len(fake.resizes))
	}
	d.handleResizePane(nil, resizeMsg(t, "p1", 120, 40))
	if len(fake.resizes) != 2 {
		t.Fatalf("PTY.Resize called %d times, want 2 (changed size must apply)", len(fake.resizes))
	}
	if pane.Cols != 120 || pane.Rows != 40 {
		t.Errorf("pane size = %dx%d, want 120x40", pane.Cols, pane.Rows)
	}
}

func TestHandleResizePane_FreshPTY_AcceptsSameSize(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &fakeSession{}
	pane := &Pane{ID: "p1", PTY: fake}
	d.session.panes["p1"] = pane

	d.handleResizePane(nil, resizeMsg(t, "p1", 100, 40))

	// Simulate restart: new PTY installed the way spawnPane does it.
	fake2 := &fakeSession{}
	pane.PluginMu.Lock()
	pane.PTY = fake2
	pane.appliedCols, pane.appliedRows = 0, 0
	pane.PluginMu.Unlock()

	d.handleResizePane(nil, resizeMsg(t, "p1", 100, 40))
	if len(fake2.resizes) != 1 {
		t.Fatalf("fresh PTY got %d resizes, want 1 (guard must reset on PTY install)", len(fake2.resizes))
	}
}

func TestHandleResizePane_NilPTY_NoApply(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	pane := &Pane{ID: "p1"}
	d.session.panes["p1"] = pane
	d.handleResizePane(nil, resizeMsg(t, "p1", 100, 40)) // must not panic
	if pane.Cols != 0 {
		t.Errorf("pane.Cols = %d, want 0 (no PTY, nothing applied)", pane.Cols)
	}
	// A later resize with a real PTY must go through even though the same
	// size was requested while the PTY was nil.
	fake := &fakeSession{}
	pane.PluginMu.Lock()
	pane.PTY = fake
	pane.PluginMu.Unlock()
	d.handleResizePane(nil, resizeMsg(t, "p1", 100, 40))
	if len(fake.resizes) != 1 {
		t.Fatalf("PTY got %d resizes, want 1 (nil-PTY request must not poison the guard)", len(fake.resizes))
	}
}

// TestSpawnPane_ResetsResizeGuard exercises the REAL reset path (spawnPane),
// not a hand-set of the fields: a fresh PTY must always accept its first
// resize even if the pane's guard still holds a stale size from the
// previous PTY. Regression cover for the same-size guard's fresh-PTY reset.
func TestSpawnPane_ResetsResizeGuard(t *testing.T) {
	// A fully-constructed test daemon: spawnPane launches streamPTYOutput,
	// whose reader hits the fakeSession's Read error and calls onPaneExit →
	// emitEvent. That path needs a real event queue (d.events) or it nil-
	// derefs; d.broadcast is already nil-safe (guards on d.server).
	d := &Daemon{
		registry: plugin.NewRegistry(),
		session:  NewSessionManager(4096),
		events:   newEventQueue(16),
	}
	pane := &Pane{ID: "p-reset", Type: "terminal"}
	// Simulate a prior PTY's last-applied size lingering on the pane.
	pane.appliedCols, pane.appliedRows = 100, 40
	// Register the pane BEFORE spawnPane so the write happens-before the
	// streamPTYOutput goroutine (which reads the session map under sm.mu);
	// writing it after spawnPane would race that reader.
	d.session.panes["p-reset"] = pane

	fake := &fakeSession{}
	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	if pane.appliedCols != 0 || pane.appliedRows != 0 {
		t.Fatalf("spawnPane left guard at %dx%d, want 0x0 (fresh PTY must accept first resize)",
			pane.appliedCols, pane.appliedRows)
	}
	// A resize at the old size now goes through to the new PTY.
	d.handleResizePane(nil, resizeMsg(t, "p-reset", 100, 40))
	if len(fake.resizes) < 1 {
		t.Errorf("fresh PTY got %d resizes at the old size, want at least 1", len(fake.resizes))
	}
}

// TestHandleResizePane_FailedResizeDoesNotStickGuard: a Resize error must
// leave the guard unchanged so the next identical broadcast retries rather
// than being silently swallowed.
func TestHandleResizePane_FailedResizeDoesNotStickGuard(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	fake := &failingResizeSession{fail: true}
	pane := &Pane{ID: "p-fail", PTY: fake}
	d.session.panes["p-fail"] = pane

	d.handleResizePane(nil, resizeMsg(t, "p-fail", 90, 30)) // fails
	if pane.appliedCols != 0 || pane.appliedRows != 0 {
		t.Fatalf("failed resize stuck the guard at %dx%d, want 0x0", pane.appliedCols, pane.appliedRows)
	}
	fake.fail = false
	d.handleResizePane(nil, resizeMsg(t, "p-fail", 90, 30)) // retry succeeds
	if pane.appliedCols != 90 || pane.appliedRows != 30 {
		t.Errorf("retry after failure did not apply: guard %dx%d, want 90x30", pane.appliedCols, pane.appliedRows)
	}
	if fake.okResizes != 1 {
		t.Errorf("successful resizes = %d, want 1 (first failed, retry succeeded)", fake.okResizes)
	}
}

// failingResizeSession fails Resize while fail is true, then succeeds.
type failingResizeSession struct {
	fakeSession
	fail      bool
	okResizes int
}

func (f *failingResizeSession) Resize(rows, cols uint16) error {
	if f.fail {
		return fmt.Errorf("simulated resize failure")
	}
	f.okResizes++
	return nil
}

// A client with no console attached is reported by Bubble Tea as 1x1, and the
// TUI's own floors (paneVTSize) turn that into a resize request that looks
// legal. Applied, it reflows every child to one column and each transcript
// re-wraps permanently. The client refuses to send it now
// (Model.terminalPaintable); this is the daemon's own floor, for an older or
// third-party client.
//
// Only BOTH dimensions at the floor together. A genuinely narrow SPLIT pane is
// narrow in ONE dimension and wide in the other — a vertical split gives few
// columns and many rows, a horizontal split the reverse — and paneVTSize floors
// at 1 precisely so those keep working. A pane that is 1x1 in both needs a
// terminal with no usable area at all.
func TestHandleResizePane_DegenerateSize_IsRefused(t *testing.T) {
	for _, tt := range []struct {
		name       string
		cols, rows uint16
		want       int
	}{
		{"one by one", 1, 1, 0},
		{"zero by zero", 0, 0, 0},
		{"narrow split column", 1, 40, 1},
		{"short split row", 100, 1, 1},
		{"ordinary pane", 100, 40, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{session: NewSessionManager(4096)}
			fake := &fakeSession{}
			// Pre-applied, so the same-size guard can never be what makes a
			// row pass. A fresh pane has appliedCols/appliedRows == 0, which
			// already matches a 0x0 payload — that row proved nothing about
			// this floor until the guard had a different size to compare with.
			// 200x50 collides with no row in the table, so every row reaches
			// the floor on its own merits.
			d.session.panes["p1"] = &Pane{
				ID: "p1", PTY: fake,
				appliedCols: 200, appliedRows: 50,
			}

			d.handleResizePane(nil, resizeMsg(t, "p1", tt.cols, tt.rows))

			if len(fake.resizes) != tt.want {
				t.Fatalf("PTY.Resize called %d times for %dx%d, want %d",
					len(fake.resizes), tt.cols, tt.rows, tt.want)
			}
		})
	}
}

// The refusal log runs once per pane per cooldown window. The client this floor
// exists for re-sends every pane's size on every broadcast, so an unthrottled
// line would rotate quild.log away — taking with it the history that explains
// the flood. That log line is the ONLY signal an operator gets that an old
// client is doing this, since the refusal itself is silent by design.
//
// Driven through handleResizePane and asserted on the LOG OUTPUT, not on
// LastDegenerateResizeAt: the timestamp is bookkeeping one step removed from
// the effect, so a stamp-but-always-log version satisfies it, and so does
// deleting the notify call from the handler or the Printf from inside it.
// Those are three separate mutations that a timestamp assertion cannot see.
func TestHandleResizePane_DegenerateResize_LogsOncePerCooldownWindow(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	d := &Daemon{session: NewSessionManager(4096)}
	d.session.panes["pane-1"] = &Pane{
		ID: "pane-1", PTY: &fakeSession{},
		appliedCols: 200, appliedRows: 50,
	}

	d.handleResizePane(nil, resizeMsg(t, "pane-1", 1, 1))
	d.handleResizePane(nil, resizeMsg(t, "pane-1", 1, 1))

	got := strings.Count(buf.String(), "refusing degenerate resize")
	if got != 1 {
		t.Fatalf("logged the refusal %d times, want exactly 1 — a client old "+
			"enough to send 1x1 re-sends every pane's size on every broadcast\n%s",
			got, buf.String())
	}
	if !strings.Contains(buf.String(), "pane-1") {
		t.Errorf("the refusal log does not name the pane:\n%s", buf.String())
	}
}

// A workspace persisted while the bug was live holds "cols": 1, "rows": 1 for
// every pane. handleResizePane's floor cannot help there — it runs when a
// client sends a size, and this happens before any client has attached — so a
// restored child would boot at one column on every daemon start thereafter.
// apty.NewWithSize floors only NON-POSITIVE values, so 1x1 survives it.
func TestNewRestoredPTY_DegenerateStoredSizeFallsBackToTheDefault(t *testing.T) {
	for _, tt := range []struct {
		name               string
		cols, rows         int
		wantCols, wantRows int
	}{
		{"poisoned by the 1x1 bug", 1, 1, 0, 0},
		{"never sized", 0, 0, 0, 0},
		{"narrow split column", 1, 40, 1, 40},
		{"short split row", 100, 1, 100, 1},
		{"ordinary pane", 172, 46, 172, 46},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotCols, gotRows int
			prev := newSessionFn
			newSessionFn = func(cols, rows int) apty.Session {
				gotCols, gotRows = cols, rows
				return &fakeSession{}
			}
			t.Cleanup(func() { newSessionFn = prev })

			newRestoredPTY(tt.cols, tt.rows)

			if gotCols != tt.wantCols || gotRows != tt.wantRows {
				t.Fatalf("newRestoredPTY(%d, %d) spawned at %dx%d, want %dx%d "+
					"(0x0 means: let apty apply its own 80x24 default)",
					tt.cols, tt.rows, gotCols, gotRows, tt.wantCols, tt.wantRows)
			}
		})
	}
}
