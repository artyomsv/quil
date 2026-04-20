package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestMemoryTree_FlattenAllCollapsed(t *testing.T) {
	resp := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TabID: "tA", TotalBytes: 100},
			{PaneID: "p2", TabID: "tA", TotalBytes: 200},
			{PaneID: "p3", TabID: "tB", TotalBytes: 50},
		},
		Total: 350,
	}
	tabOrder := []string{"tA", "tB"}
	tabNames := map[string]string{"tA": "Shell", "tB": "Build"}
	tree := buildMemoryTree(resp, tabOrder, tabNames)

	// All tabs start collapsed — only top-line + tab rows visible.
	rows := tree.flatten()
	// 1 total + 2 tab rows = 3
	if len(rows) != 3 {
		t.Errorf("flatten(collapsed) = %d rows, want 3", len(rows))
	}
	// tA total = 300, tB total = 50. tA must come first (Total desc).
	if rows[1].label != "Shell" || rows[2].label != "Build" {
		t.Errorf("tab order wrong: %q, %q", rows[1].label, rows[2].label)
	}
}

func TestMemoryTree_ExpandTab(t *testing.T) {
	resp := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TabID: "tA", TotalBytes: 100},
			{PaneID: "p2", TabID: "tA", TotalBytes: 200},
		},
		Total: 300,
	}
	tree := buildMemoryTree(resp, []string{"tA"}, map[string]string{"tA": "Shell"})
	tree.toggleAt(1) // expand tA
	rows := tree.flatten()
	// 1 total + 1 tab + 2 panes = 4
	if len(rows) != 4 {
		t.Errorf("flatten(expanded) = %d rows, want 4", len(rows))
	}
	// panes sorted by Total desc — p2 first.
	if rows[2].label != "p2" || rows[3].label != "p1" {
		t.Errorf("pane order wrong: %q, %q", rows[2].label, rows[3].label)
	}
}
