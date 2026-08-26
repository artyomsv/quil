package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/proctree"
)

// Tab and Total rows used to hardcode UnknownCPU, so a workspace with every tab
// collapsed — the state the dialog opens in — showed an em dash in every CPU
// cell it had. These pin that the aggregates are real, and that a partial sum
// says so rather than passing itself off as complete.

func cpuNode(pid int, pct float64, children ...ipc.ProcNode) ipc.ProcNode {
	return ipc.ProcNode{PID: pid, Name: "p", CPUPct: pct, Depth: 1, Children: children}
}

// twoTabModel builds a report with two tabs: tab-1 has two panes (3% and 4%),
// tab-2 has one pane whose tree is entirely unsampled.
func twoTabModel(t *testing.T) Model {
	t.Helper()
	m := Model{lastWidth: 200}
	m = m.openProcessesDialog()
	return m.applyResourceReport(ipc.ResourceReportRespPayload{
		WithTrees: true,
		Panes: []ipc.PaneResourceInfo{
			{
				PaneID: "pane-a", TabID: "tab-1", TotalBytes: 100,
				Tree: ptrNode(cpuNode(10, 1.0, cpuNode(11, 2.0))),
			},
			{
				PaneID: "pane-b", TabID: "tab-1", TotalBytes: 100,
				Tree: ptrNode(cpuNode(20, 4.0)),
			},
			{
				PaneID: "pane-c", TabID: "tab-2", TotalBytes: 100,
				Tree: ptrNode(cpuNode(30, proctree.UnknownCPU)),
			},
		},
	})
}

func ptrNode(n ipc.ProcNode) *ipc.ProcNode { return &n }

func rowOfKind(rows []procRow, kind procRowKind, tabID string) (procRow, bool) {
	for _, r := range rows {
		if r.kind == kind && (tabID == "" || r.tabID == tabID) {
			return r, true
		}
	}
	return procRow{}, false
}

func TestProcRows_TabRowSumsItsPanesCPU(t *testing.T) {
	rows := twoTabModel(t).procRows()

	r, ok := rowOfKind(rows, procRowTab, "tab-1")
	if !ok {
		t.Fatal("no row for tab-1")
	}
	// 1 + 2 + 4
	if r.cpu < 6.99 || r.cpu > 7.01 {
		t.Errorf("tab-1 cpu = %v, want 7", r.cpu)
	}
}

// A tab whose processes have not been sampled twice yet must stay unknown, not
// collapse to 0 — the same distinction procTreeCPU already keeps one level down.
func TestProcRows_TabWithNoSampledProcessesStaysUnknown(t *testing.T) {
	rows := twoTabModel(t).procRows()

	r, ok := rowOfKind(rows, procRowTab, "tab-2")
	if !ok {
		t.Fatal("no row for tab-2")
	}
	if r.cpu >= 0 {
		t.Errorf("tab-2 cpu = %v, want unknown — none of its processes had an answer", r.cpu)
	}
}

func TestProcRows_TotalRowSumsEveryPane(t *testing.T) {
	rows := twoTabModel(t).procRows()

	r, ok := rowOfKind(rows, procRowTotal, "")
	if !ok {
		t.Fatal("no Total row")
	}
	if r.cpu < 6.99 || r.cpu > 7.01 {
		t.Errorf("total cpu = %v, want 7 (the unsampled pane contributes nothing)", r.cpu)
	}
}

// The honesty rule for aggregates. Summing only the known values while some
// children are unknown UNDER-reports, and a bare "7%" claims completeness the
// number does not have. The marker is what separates the two.
func TestProcRows_PartialTotalIsMarkedIncomplete(t *testing.T) {
	rows := twoTabModel(t).procRows()

	r, ok := rowOfKind(rows, procRowTotal, "")
	if !ok {
		t.Fatal("no Total row")
	}
	if !r.cpuPartial {
		t.Error("total is summed over a set containing unsampled processes but " +
			"is not marked partial — it would render as a complete figure")
	}

	line := renderProcRow(r, false, 120)
	if !strings.Contains(line, "~") {
		t.Errorf("partial total rendered without an incompleteness marker: %q", line)
	}
}

func TestProcRows_CompleteTabIsNotMarkedPartial(t *testing.T) {
	rows := twoTabModel(t).procRows()

	r, ok := rowOfKind(rows, procRowTab, "tab-1")
	if !ok {
		t.Fatal("no row for tab-1")
	}
	if r.cpuPartial {
		t.Error("tab-1 has an answer for every process but is marked partial")
	}
	line := renderProcRow(r, false, 120)
	if strings.Contains(line, "~") {
		t.Errorf("complete tab rendered an incompleteness marker: %q", line)
	}
}

// A collapsed pane used to report unknown so its subtree was never walked on
// render. The totals are computed once when the report arrives instead, so the
// number is available whether or not the row is open.
func TestProcRows_CollapsedPaneStillShowsItsSubtotal(t *testing.T) {
	m := twoTabModel(t)
	m.proc.expandedTabs["tab-1"] = true // tab open, panes still collapsed
	rows := m.procRows()

	var pane *procRow
	for i := range rows {
		if rows[i].kind == procRowPane && rows[i].paneID == "pane-a" {
			pane = &rows[i]
			break
		}
	}
	if pane == nil {
		t.Fatal("no row for pane-a")
	}
	if pane.expanded {
		t.Fatal("pane-a should be collapsed for this test")
	}
	if pane.cpu < 2.99 || pane.cpu > 3.01 {
		t.Errorf("collapsed pane cpu = %v, want 3 (1 + 2)", pane.cpu)
	}
}

func TestFormatCPUAggregate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pct     float64
		partial bool
		want    string
	}{
		{"unknown", proctree.UnknownCPU, false, "—"},
		{"unknown stays unknown when partial", proctree.UnknownCPU, true, "—"},
		{"complete", 36.4, false, "36%"},
		{"partial", 36.4, true, "~36%"},
		{"complete zero", 0, false, "0%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatCPUAggregate(tc.pct, tc.partial); got != tc.want {
				t.Errorf("formatCPUAggregate(%v, %v) = %q, want %q",
					tc.pct, tc.partial, got, tc.want)
			}
		})
	}
}

// The aggregates must survive the width budget like every other row.
func TestRenderProcRow_AggregateRowsFitNarrowBudgets(t *testing.T) {
	rows := twoTabModel(t).procRows()
	for _, inner := range []int{40, 60, 80, 120} {
		for _, r := range rows {
			line := renderProcRow(r, false, inner)
			if w := lipgloss.Width(line); w > inner {
				t.Errorf("inner=%d produced a %d-cell row (kind=%v): %q",
					inner, w, r.kind, line)
			}
		}
	}
}
