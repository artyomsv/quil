package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ringbuf"
)

// TestSnapshotRestore_HistoryLineCount verifies HistoryLines is snapshotted from
// the on-disk ghost buffer during restore (the count the checklist's "history
// restored (N ln)" row shows). Round-trips a terminal pane's output through
// snapshot → restore.
func TestSnapshotRestore_HistoryLineCount(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "terminal"                                // GhostBuffer-enabled, so snapshot persists the buffer
	pane.OutputBuf.Write([]byte("line1\nline2\nline3\n")) // 3 newlines

	d.snapshot()

	d2 := New(config.Default())
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}
	restored := d2.session.Pane(pane.ID)
	if restored == nil {
		t.Fatalf("pane %s not restored", pane.ID)
	}
	if restored.HistoryLines != 3 {
		t.Errorf("restored HistoryLines = %d, want 3", restored.HistoryLines)
	}
}

// paneMapByID pulls the pane entries out of a workspace-state map keyed by id.
// Only valid for a map returned directly by workspaceStateFromSnapshot — after a
// JSON round-trip "panes" would be []any, not []map[string]any.
func paneMapByID(t *testing.T, state map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	panes, ok := state["panes"].([]map[string]any)
	if !ok {
		t.Fatalf("state[panes] wrong type: %T", state["panes"])
	}
	for _, p := range panes {
		out[p["id"].(string)] = p
	}
	return out
}

func TestWorkspaceState_BroadcastsSessionIDAndHistoryLines(t *testing.T) {
	d := New(config.Default())
	pane := &Pane{
		ID: "pane-aa", TabID: "tab-aa", Type: "claude-code",
		OutputBuf:    ringbuf.NewRingBuffer(d.session.bufSize),
		HistoryLines: 3,
		PluginState:  map[string]string{"session_id": "8f2e1c00-dead-beef"},
	}
	d.session.RestoreTab(&Tab{ID: "tab-aa", Name: "A", Panes: []string{"pane-aa"}}, []*Pane{pane})

	bc := paneMapByID(t, d.buildWorkspaceState())["pane-aa"]
	if bc["session_id"] != "8f2e1c00-dead-beef" {
		t.Errorf("broadcast session_id = %v, want full id", bc["session_id"])
	}
	if hl, _ := bc["history_lines"].(int); hl != 3 {
		t.Errorf("broadcast history_lines = %v, want 3", bc["history_lines"])
	}

	active, tabs, byTab := d.session.SnapshotState()
	disk := paneMapByID(t, d.workspaceStateFromSnapshot(active, tabs, byTab, false))["pane-aa"]
	if _, ok := disk["session_id"]; ok {
		t.Error("disk snapshot must not contain session_id")
	}
	if _, ok := disk["history_lines"]; ok {
		t.Error("disk snapshot must not contain history_lines")
	}
}

func TestWorkspaceState_OmitsEmptyChecklistHints(t *testing.T) {
	d := New(config.Default())
	pane := &Pane{
		ID: "pane-bb", TabID: "tab-bb", Type: "terminal",
		OutputBuf: ringbuf.NewRingBuffer(d.session.bufSize),
	}
	d.session.RestoreTab(&Tab{ID: "tab-bb", Name: "B", Panes: []string{"pane-bb"}}, []*Pane{pane})
	bc := paneMapByID(t, d.buildWorkspaceState())["pane-bb"]
	if _, ok := bc["session_id"]; ok {
		t.Error("session_id must be omitted when empty")
	}
	if _, ok := bc["history_lines"]; ok {
		t.Error("history_lines must be omitted when zero")
	}
}
