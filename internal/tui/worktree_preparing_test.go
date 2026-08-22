package tui

import (
	"strings"
	"testing"
	"time"
)

// The placeholder says WHAT it is waiting for, in its own rectangle.
//
// It used to be a live shell in the repository root — indistinguishable from a
// create that finished and put the user in the wrong tree — for the whole of a
// checkout that is minutes on a large monorepo.
func TestPaneView_PreparingWorktreeIsShownInThePane(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.PreparingWorktree = "fix/nationality-filter"
	p.Width, p.Height = 60, 12

	out := stripANSI(p.View())
	if !strings.Contains(out, "fix/nationality-filter") {
		t.Errorf("pane render does not name the branch being checked out:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "worktree") {
		t.Errorf("pane render does not say what it is waiting for:\n%s", out)
	}
}

// A pane preparing a worktree must not also claim to be a broken one. SpawnError
// is the TERMINAL state — the daemon writes it when the add fails, clearing the
// branch in the same step — so the two can never be true at once, and if they
// ever were the failure has to win.
func TestPaneView_SpawnErrorOutranksPreparing(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.PreparingWorktree = "fix/x"
	p.SpawnError = "worktree not created: fatal: a branch named 'fix/x' already exists"
	p.Width, p.Height = 70, 12

	out := stripANSI(p.View())
	if !strings.Contains(out, "already exists") {
		t.Errorf("the failure was hidden behind the preparing indicator:\n%s", out)
	}
}

// Sanitized and bounded like every other daemon-authored string drawn straight
// into the frame: a branch name comes back from a host the user may not control
// under --remote, and lipgloss.Place pads but never clips.
func TestPaneView_SanitizesThePreparingBranch(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.PreparingWorktree = "feat/\x1b]52;c;cGF5bG9hZA==\x07x"
	p.Width, p.Height = 60, 12

	if strings.Contains(p.View(), "\x1b]52") {
		t.Error("an OSC 52 in the branch name survived to the rendered pane")
	}
}

func TestPaneView_PreparingNeverOutgrowsThePane(t *testing.T) {
	long := strings.Repeat("feat/very-long-branch-name-segment/", 8)
	for _, size := range []struct{ w, h int }{
		{0, 0}, {1, 1}, {3, 2}, {5, 3}, {10, 5}, {24, 8}, {80, 24},
	} {
		p := NewPaneModel("p1", 1024)
		p.PreparingWorktree = long
		p.Width, p.Height = size.w, size.h

		clean := NewPaneModel("p1", 1024)
		clean.Width, clean.Height = size.w, size.h
		want := widestLine(stripANSI(clean.View()))

		if got := widestLine(stripANSI(p.View())); got > want {
			t.Errorf("%dx%d: pane is %d cells while preparing, %d without — the block overflows its rect",
				size.w, size.h, got, want)
		}
	}
}

// Unconditional copy, like SpawnError beside it: the pane that replaces this one
// carries no branch, and a guarded copy would leave a finished checkout spinning.
func TestSyncPaneMeta_CarriesAndClearsPreparingWorktree(t *testing.T) {
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", PreparingWorktree: "feat/x"}, false, 0, false)
	if pane.PreparingWorktree != "feat/x" {
		t.Fatalf("PreparingWorktree = %q, want the daemon's branch", pane.PreparingWorktree)
	}

	syncPaneMeta(pane, &PaneInfo{ID: "p1"}, false, 0, false)
	if pane.PreparingWorktree != "" {
		t.Errorf("PreparingWorktree = %q after a clean update, want it cleared", pane.PreparingWorktree)
	}
}

// The spinner must outlive restoreSafetyCap. That cap bounds a pane BOOT, which
// is seconds; a `git worktree add` against a large monorepo runs for minutes and
// the daemon allows it two, so a frozen glyph would be the exact "is this thing
// doing anything" question the indicator exists to answer.
func TestSpinnerTick_PreparingWorktreeOutlivesTheRestoreCap(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.PreparingWorktree = "feat/x"
	p.resumeStart = time.Now().Add(-2 * restoreSafetyCap)

	if !p.spinnerRunning() {
		t.Error("the spinner stopped while a checkout was still running")
	}
}

// And it stops once there is nothing to wait for, or the tick chain runs for the
// life of the session.
func TestSpinnerTick_StopsWhenThePreparationSettles(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.resumeStart = time.Now().Add(-2 * restoreSafetyCap)

	if p.spinnerRunning() {
		t.Error("the spinner kept ticking for a pane that is preparing nothing")
	}
}

// The render cache is keyed on everything the frame draws. Without the branch in
// the key the placeholder paints once and then serves a stale frame — including
// through the swap, so the finished pane would keep showing "creating worktree".
func TestPaneRenderKey_ChangesWithThePreparingBranch(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.Width, p.Height = 40, 10
	before := p.renderKey()
	p.PreparingWorktree = "feat/x"

	if p.renderKey() == before {
		t.Error("renderKey ignores PreparingWorktree — the pane would serve a stale frame")
	}
}

// SpawnError joins the key for the same reason, and it did not used to need to:
// it was only ever set during restore, BEFORE a pane's first frame. A failed
// worktree create writes it onto a pane that has already rendered, and Alt+R
// then clears it with no output to bump contentGen — so without this the pane
// keeps painting the error over a shell that is already running.
func TestPaneRenderKey_ChangesWithTheSpawnError(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.Width, p.Height = 40, 10
	p.SpawnError = "worktree not created: fatal: already exists"
	before := p.renderKey()
	p.SpawnError = ""

	if p.renderKey() == before {
		t.Error("renderKey ignores SpawnError — a retried pane keeps painting the old failure")
	}
}
