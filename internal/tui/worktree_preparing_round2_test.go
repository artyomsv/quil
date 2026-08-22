package tui

import (
	"strings"
	"testing"
	"time"
)

// The width-only assertion in TestPaneView_PreparingNeverOutgrowsThePane passes
// against a block that is too TALL, which is the exact "the test measures the
// wrong axis" trap — and the block was too tall.
//
// lipgloss.Place pads but never CLIPS (renderSpawnError's own comment says so,
// which is why it gates its extras on innerH). View emits one manual top-border
// line plus bodyStyle.Height(innerH+1), so a pane's whole budget is
// max(Height, 3) lines. A block one line over shifts every sibling in the tab's
// JoinHorizontal.
func TestPaneView_PreparingNeverOutgrowsThePaneHeight(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{0, 0}, {1, 1}, {10, 2}, {24, 3}, {40, 4}, {60, 8}, {80, 24},
	} {
		p := NewPaneModel("p1", 1024)
		p.PreparingWorktree = "fix/nationality-filter"
		p.Width, p.Height = size.w, size.h

		// Against the pane's OWN rect, not against a clean pane: a clean pane
		// could itself be over budget and would then mask this. View emits one
		// manual top-border line plus bodyStyle.Height(innerH+1), and innerH
		// floors at 1, so the budget is max(Height, 3).
		want := size.h
		if want < 3 {
			want = 3
		}
		errPane := NewPaneModel("p1", 1024)
		errPane.SpawnError = "worktree not created: fatal: a branch named 'x' already exists"
		errPane.Width, errPane.Height = size.w, size.h
		errLines := len(strings.Split(stripANSI(errPane.View()), "\n"))

		got := len(strings.Split(stripANSI(p.View()), "\n"))
		t.Logf("%dx%d: budget=%d preparing=%d spawnError=%d", size.w, size.h, want, got, errLines)
		if got > want {
			t.Errorf("%dx%d: pane is %d lines while preparing, budget is %d — the block overflows its rect",
				size.w, size.h, got, want)
		}
	}
}

// The branch is what the user needs; the reassurance line is what gets dropped.
// Asserted so a future edit cannot "fix" the overflow by dropping the branch.
func TestPaneView_PreparingKeepsTheBranchWhenShort(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.PreparingWorktree = "fix/nat"
	p.Width, p.Height = 40, 4

	if out := stripANSI(p.View()); !strings.Contains(out, "fix/nat") {
		t.Errorf("a short pane dropped the branch name:\n%s", out)
	}
}

// The two spinner tests added in round 1 call p.spinnerRunning() DIRECTLY, so
// both pass unchanged against an Update arm that still reads the old
// `(resuming||preparing) && !restoreSettled()` expression — the "decision
// function tested, call site not" trap this project documents.
//
// Driven through Update: a non-nil Cmd is the tick chain re-arming, which is the
// behaviour that actually keeps the glyph moving.
func TestUpdate_SpinnerTickReArmsForAWorktreeCheckout(t *testing.T) {
	m := newSplitDragTestModel(t)
	tab := m.activeTabModel()
	pane := tab.Leaves()[0]
	pane.PreparingWorktree = "feat/x"
	// Older than every restore cap, so only the worktree branch of
	// spinnerRunning can keep the chain alive.
	pane.resumeStart = time.Now().Add(-2 * restoreSafetyCap)

	_, cmd := m.Update(spinnerTickMsg{paneID: pane.ID, frame: 3})
	if cmd == nil {
		t.Error("the tick chain stopped while a checkout was still running — the glyph would freeze in front of live work")
	}
}

// And the same arm must let go once the daemon clears the branch, or the chain
// runs for the life of the session.
func TestUpdate_SpinnerTickStopsWhenTheCheckoutSettles(t *testing.T) {
	m := newSplitDragTestModel(t)
	tab := m.activeTabModel()
	pane := tab.Leaves()[0]
	pane.resumeStart = time.Now().Add(-2 * restoreSafetyCap)

	if _, cmd := m.Update(spinnerTickMsg{paneID: pane.ID, frame: 3}); cmd != nil {
		t.Error("the tick chain re-armed for a pane that is preparing nothing")
	}
}
