package main

import (
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestBuildTabMemSummaries_AggregatesPerTab covers the happy path: every
// pane in mem.Panes maps to a known tab in the embedded Tabs slice.
// goHeap / ptyRSS totals must equal the sum across all panes; per-tab
// counts and totals must respect the tabOrder from the Tabs argument.
func TestBuildTabMemSummaries_AggregatesPerTab(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "pane-aaa", TabID: "tab-1", GoHeapBytes: 1000, PTYRSSBytes: 0, TotalBytes: 1000},
			{PaneID: "pane-bbb", TabID: "tab-1", GoHeapBytes: 500, PTYRSSBytes: 2000, TotalBytes: 2500},
			{PaneID: "pane-ccc", TabID: "tab-2", GoHeapBytes: 100, PTYRSSBytes: 4000, TotalBytes: 4100},
		},
	}
	tabs := []ipc.TabInfo{
		{ID: "tab-1", Name: "Build", PaneCount: 2},
		{ID: "tab-2", Name: "Notes", PaneCount: 1},
	}

	goHeap, ptyRSS, summaries := buildTabMemSummaries(mem, tabs)

	if goHeap != 1600 {
		t.Errorf("goHeap = %d, want 1600", goHeap)
	}
	if ptyRSS != 6000 {
		t.Errorf("ptyRSS = %d, want 6000", ptyRSS)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(summaries))
	}

	if summaries[0].TabID != "tab-1" || summaries[0].TabName != "Build" {
		t.Errorf("summaries[0] = %+v, want tab-1/Build", summaries[0])
	}
	if summaries[0].PaneCount != 2 || summaries[0].TotalBytes != 3500 {
		t.Errorf("summaries[0] count/total = %d/%d, want 2/3500", summaries[0].PaneCount, summaries[0].TotalBytes)
	}
	if summaries[1].TabID != "tab-2" || summaries[1].TotalBytes != 4100 {
		t.Errorf("summaries[1] = %+v, want tab-2 / 4100 bytes", summaries[1])
	}

	// TotalHuman is informative but stable for these inputs.
	if summaries[0].TotalHuman == "" || summaries[1].TotalHuman == "" {
		t.Errorf("TotalHuman should be non-empty for both summaries")
	}
}

// TestBuildTabMemSummaries_OrphanPaneFallback handles the case where a pane
// references a tab that isn't in the Tabs slice — typical when the tab was
// destroyed between the memreport tick and the Tabs snapshot. The orphan
// must still appear in summaries (no data lost) using the bare TabID as
// the display name.
func TestBuildTabMemSummaries_OrphanPaneFallback(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "pane-ghost", TabID: "tab-removed", GoHeapBytes: 100, TotalBytes: 100},
		},
	}
	tabs := []ipc.TabInfo{} // empty — the tab is gone

	_, _, summaries := buildTabMemSummaries(mem, tabs)

	if len(summaries) != 1 {
		t.Fatalf("summaries len = %d, want 1", len(summaries))
	}
	if summaries[0].TabID != "tab-removed" || summaries[0].TabName != "tab-removed" {
		t.Errorf("orphan summary = %+v, want TabID and TabName both 'tab-removed'", summaries[0])
	}
	if summaries[0].PaneCount != 1 || summaries[0].TotalBytes != 100 {
		t.Errorf("orphan count/total = %d/%d, want 1/100", summaries[0].PaneCount, summaries[0].TotalBytes)
	}
}

// TestBuildTabMemSummaries_NilTabsBackwardCompat verifies the pre-1.10 daemon
// case where MemoryReportRespPayload.Tabs is nil. The tool must still
// produce one summary per (orphan-treated) tab seen on a pane, falling
// back to TabID as the name. This is the safety net that lets the MCP
// bridge keep working during a rolling daemon upgrade.
func TestBuildTabMemSummaries_NilTabsBackwardCompat(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TabID: "tab-1", GoHeapBytes: 50, TotalBytes: 50},
			{PaneID: "p2", TabID: "tab-2", GoHeapBytes: 70, TotalBytes: 70},
		},
	}

	_, _, summaries := buildTabMemSummaries(mem, nil)

	gotIDs := make([]string, len(summaries))
	for i, s := range summaries {
		gotIDs[i] = s.TabID
		if s.TabName != s.TabID {
			t.Errorf("summary %s: name = %q, want fallback to TabID", s.TabID, s.TabName)
		}
	}
	want := []string{"tab-1", "tab-2"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("orphan order = %v, want %v", gotIDs, want)
	}
}

// TestBuildTabMemSummaries_EmptyMemKeepsTabsAsZeroRows verifies that tabs
// with no panes still appear in the summary list with PaneCount=0 — the
// memory dialog should show every tab even when it's empty.
func TestBuildTabMemSummaries_EmptyMemKeepsTabsAsZeroRows(t *testing.T) {
	mem := ipc.MemoryReportRespPayload{} // no panes
	tabs := []ipc.TabInfo{
		{ID: "tab-1", Name: "Build"},
		{ID: "tab-2", Name: "Notes"},
	}

	goHeap, ptyRSS, summaries := buildTabMemSummaries(mem, tabs)

	if goHeap != 0 || ptyRSS != 0 {
		t.Errorf("goHeap/ptyRSS = %d/%d, want 0/0", goHeap, ptyRSS)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(summaries))
	}
	for _, s := range summaries {
		if s.PaneCount != 0 || s.TotalBytes != 0 {
			t.Errorf("empty tab %s: count/total = %d/%d, want 0/0", s.TabID, s.PaneCount, s.TotalBytes)
		}
	}
}
