package tui

import (
	"strings"
	"testing"
)

// The message is rendered in the pane's OWN rect, not as a modal: restore can
// produce several of these at once and a modal each would be unusable.
func TestPaneView_SpawnErrorIsShownInThePane(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.SpawnError = "worktree is gone: /wt/feat-x"
	p.Width, p.Height = 60, 12

	out := stripANSI(p.View())
	if !strings.Contains(out, "worktree is gone") {
		t.Errorf("pane render does not carry the reason:\n%s", out)
	}
	if !strings.Contains(out, "Alt+R") {
		t.Errorf("pane render does not offer the retry:\n%s", out)
	}
}

// A pane with no error renders its content, not the error block — the check
// must not fire on the ordinary case.
func TestPaneView_NoSpawnErrorRendersNormally(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.Width, p.Height = 60, 12

	if strings.Contains(stripANSI(p.View()), "Alt+R to retry") {
		t.Error("a healthy pane rendered the spawn-error block")
	}
}

// The message interpolates a path from a daemon the user may not control, and
// this text is drawn straight into the frame rather than through the VT
// emulator that makes pane output safe.
func TestPaneView_SanitizesTheSpawnError(t *testing.T) {
	p := NewPaneModel("p1", 1024)
	p.SpawnError = "gone: \x1b]52;c;cGF5bG9hZA==\x07"
	p.Width, p.Height = 60, 12

	if strings.Contains(p.View(), "\x1b]52") {
		t.Error("an OSC 52 in the spawn error survived to the rendered pane")
	}
}

// Copied unconditionally so a pane whose worktree comes back loses its
// complaint — the daemon clears the field on every successful spawn, and a
// guarded copy would keep showing the old one.
func TestSyncPaneMeta_ClearsTheSpawnError(t *testing.T) {
	pane := &PaneModel{ID: "p1", SpawnError: "worktree is gone: /wt/feat-x"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1"}, false, 0)

	if pane.SpawnError != "" {
		t.Errorf("SpawnError = %q after a clean update, want it cleared", pane.SpawnError)
	}
}

func TestSyncPaneMeta_CarriesTheSpawnError(t *testing.T) {
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", SpawnError: "worktree is gone: /wt/feat-x"}, false, 0)

	if pane.SpawnError != "worktree is gone: /wt/feat-x" {
		t.Errorf("SpawnError = %q, want the daemon's message", pane.SpawnError)
	}
}
