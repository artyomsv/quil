package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// The PR fields cross two hand-written copies on their way to the row —
// ipc.PaneState → PaneInfo (parseWorkspaceState) and PaneInfo → PaneModel
// (syncPaneMeta) — and a field missing from either is dropped silently while
// every helper test stays green. These tests feed a real frame through the
// production decode/apply path and read the row.

func prFrame(t *testing.T, pane map[string]any) WorkspaceStateMsg {
	t.Helper()
	pane["id"] = "p1"
	pane["tab_id"] = "t1"
	pane["cwd"] = "/r"
	return parseWorkspaceState(stateFromMap(t, map[string]any{
		"active_tab":     "t1",
		"active_project": "proj-a",
		"tabs":           []any{map[string]any{"id": "t1", "name": "build", "panes": []any{"p1"}, "project_id": "proj-a"}},
		"panes":          []any{pane},
		"projects":       []any{map[string]any{"id": "proj-a", "name": "A", "root_dir": "/r", "tab_ids": []any{"t1"}, "active_tab": "t1"}},
	}))
}

func TestParseWorkspaceState_ReadsThePRWireKeys(t *testing.T) {
	t.Parallel()
	got := prFrame(t, map[string]any{"git_branch": "feat/x", "pr_number": 277, "pr_status": "approved", "pr_stale": true})
	if len(got.Panes) != 1 {
		t.Fatalf("parsed %d panes, want 1", len(got.Panes))
	}
	p := got.Panes[0]
	if p.PRNumber != 277 || p.PRStatus != "approved" || !p.PRStale {
		t.Fatalf("PaneInfo PR fields = %d %q stale=%v; the daemon writes pr_number/pr_status/pr_stale", p.PRNumber, p.PRStatus, p.PRStale)
	}
}

func TestSyncPaneMeta_CopiesAndClearsThePRFields(t *testing.T) {
	t.Parallel()
	pane := &PaneModel{ID: "p1"}
	syncPaneMeta(pane, &PaneInfo{ID: "p1", PRNumber: 277, PRStatus: "running", PRStale: true}, false, 0, false, false)
	if pane.PRNumber != 277 || pane.PRStatus != "running" || !pane.PRStale {
		t.Fatalf("PaneModel PR fields = %d %q stale=%v after a frame carrying them", pane.PRNumber, pane.PRStatus, pane.PRStale)
	}
	// Unconditional: the absent key is what removes a PR row that no
	// longer applies (a NoPR answer, a branch change, the feature turned off).
	syncPaneMeta(pane, &PaneInfo{ID: "p1"}, false, 0, false, false)
	if pane.PRNumber != 0 || pane.PRStatus != "" || pane.PRStale {
		t.Fatalf("PaneModel PR fields = %d %q stale=%v after a frame WITHOUT them, want all cleared", pane.PRNumber, pane.PRStatus, pane.PRStale)
	}
}

// End to end: a frame on the production apply path puts the digits on the
// sidebar row; the next frame without the keys takes them off again.
func TestPRChip_AppearsFromAFrameAndLeavesWhenTheKeysDo(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{
		cfg: config.Default(), width: 120, height: 40,
		notifications: NewNotificationCenter(30, 50),
		sidebarOpen:   true, sidebarWidth: defaultSidebarWidth,
	}

	m.applyWorkspaceState(prFrame(t, map[string]any{"git_branch": "feat/x", "pr_number": 277, "pr_status": "approved", "pr_stale": true}), "")
	pane := m.findPane("p1")
	if pane == nil {
		t.Fatal("pane p1 not built from the frame")
	}
	if row := stripANSI(prRow(pane, defaultSidebarWidth)); !strings.Contains(row, "#277 approved ~") {
		t.Fatalf("PR row %q lacks the stale \"#277 approved ~\" after a frame carrying it", row)
	}

	m.applyWorkspaceState(prFrame(t, map[string]any{"git_branch": "feat/x"}), "")
	pane = m.findPane("p1")
	if row := prRow(pane, defaultSidebarWidth); row != "" {
		t.Fatalf("PR row %q still shown after a frame without the PR keys", row)
	}

	m.applyWorkspaceState(prFrame(t, map[string]any{"git_branch": "feat/x", "pr_number": 9, "pr_status": "no-such-status"}), "")
	pane = m.findPane("p1")
	row := prRow(pane, defaultSidebarWidth)
	if !strings.Contains(stripANSI(row), "#9 open") {
		t.Fatalf("PR row %q: an unknown status name must render as open", stripANSI(row))
	}
	if sgr := styleSGR(t, prRowStyle("open")); sgr != "" && !strings.Contains(row, sgr) {
		t.Fatalf("PR row %q renders an unknown status in something other than open's style", row)
	}
}
