package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/proctree"
)

// procReport builds a report with one tab, one pane and a three-deep tree.
func procReport() ipc.ResourceReportRespPayload {
	start := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	return ipc.ResourceReportRespPayload{
		SnapshotAt:   time.Now().UnixNano(),
		TreesAt:      time.Now().UnixNano(),
		Total:        5 << 30,
		CPUSupported: true,
		CPUSampled:   true,
		Quil: []ipc.QuilProcInfo{
			{Role: "tui", PID: 3120, Version: "1.62.6", ExeName: "quil.exe", UptimeMS: 8_000_000},
			{Role: "daemon", PID: 2044, Version: "1.62.6", ExeName: "quild.exe", UptimeMS: 500_000_000},
			{Role: "bridge", PID: 5044, Version: "1.62.4", ExeName: "quil.exe.old.3", UptimeMS: 200_000_000, Stale: true},
		},
		Unidentified: 1,
		Panes: []ipc.PaneResourceInfo{{
			PaneID:      "pane-a",
			TabID:       "tab-1",
			PTYRSSBytes: 4 << 30,
			TotalBytes:  4 << 30,
			Tree: &ipc.ProcNode{
				PID: 4812, Name: "zsh", Depth: 1, CPUPct: 1, RSSBytes: 1 << 20,
				StartMS: start.UnixMilli(),
				Children: []ipc.ProcNode{{
					PID: 5219, Name: "node vite build", Depth: 2, CPUPct: 36, RSSBytes: 3 << 30,
					StartMS: start.Add(10 * time.Second).UnixMilli(),
					Children: []ipc.ProcNode{{
						PID: 5301, Name: "esbuild", Depth: 3,
						CPUPct:   proctree.UnknownCPU,
						RSSBytes: 190 << 20,
						StartMS:  start.Add(20 * time.Second).UnixMilli(),
					}},
				}},
			},
		}},
	}
}

// procModel returns a Model with the dialog open and a report applied, fully
// expanded so process rows are visible.
func procModel() Model {
	m := Model{lastWidth: 200}
	m = m.openProcessesDialog()
	m.proc.expandedTabs["tab-1"] = true
	m.proc.expandedPanes["pane-a"] = true
	m = m.applyResourceReport(procReport())
	return m
}

func rowWithPID(rows []procRow, pid int) (procRow, bool) {
	for _, r := range rows {
		if r.pid == pid && r.kind == procRowProc {
			return r, true
		}
	}
	return procRow{}, false
}

func TestProcRows_DepthDrivesKillability(t *testing.T) {
	rows := procModel().procRows()

	shell, ok := rowWithPID(rows, 4812)
	if !ok {
		t.Fatal("pane's own child missing from rows")
	}
	if shell.killable {
		t.Error("the pane's own shell is killable; that is restart-pane, and " +
			"offering it here widens the blast radius the design bounds")
	}

	for _, pid := range []int{5219, 5301} {
		r, ok := rowWithPID(rows, pid)
		if !ok {
			t.Fatalf("descendant %d missing from rows", pid)
		}
		if !r.killable {
			t.Errorf("descendant %d is not killable; it was started inside the "+
				"user's own pane, which is exactly what this dialog stops", pid)
		}
	}
}

// The convention that has to survive all the way to the screen: a process with
// no CPU answer renders as an em dash, never as 0%.
func TestRenderProcRow_UnknownCPURendersEmDash(t *testing.T) {
	rows := procModel().procRows()
	r, ok := rowWithPID(rows, 5301)
	if !ok {
		t.Fatal("esbuild row missing")
	}

	line := renderProcRow(r, false, 80)
	if strings.Contains(line, "0%") {
		t.Errorf("unknown CPU rendered as a percentage: %q — that reads as idle, "+
			"the wrong claim in a dialog for finding things that spin", line)
	}
	if !strings.Contains(line, "—") {
		t.Errorf("unknown CPU did not render an em dash: %q", line)
	}
}

func TestFormatCPU(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{proctree.UnknownCPU, "—"},
		{-5, "—"},
		{0, "0%"},
		{36.4, "36%"},
		{200, "200%"},
	} {
		if got := formatCPU(tc.in); got != tc.want {
			t.Errorf("formatCPU(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Every row must fit the dialog's content budget across geometries. The removed
// version emitted 89-cell rows into an 86-cell budget, so every row wrapped and
// the dialog rendered at double height.
func TestRenderProcRow_FitsContentBudgetAcrossGeometries(t *testing.T) {
	m := procModel()
	for _, termW := range []int{200, 120, 100, 80, 60} {
		t.Run(fmt.Sprintf("term=%d", termW), func(t *testing.T) {
			m.lastWidth = termW
			inner := dialogInnerWidth(termW, processesDialogWidth)
			for i, row := range m.procRows() {
				line := renderProcRow(row, false, inner)
				if w := lipgloss.Width(line); w > inner {
					t.Errorf("row %d is %d cells against a %d-cell budget: %q",
						i, w, inner, line)
				}
			}
		})
	}
}

func TestProcRows_UnidentifiedLineIsShown(t *testing.T) {
	rows := procModel().procRows()
	for _, r := range rows {
		if r.kind == procRowUnidentified {
			if !strings.Contains(r.label, "1 unidentified") {
				t.Errorf("unidentified row reads %q", r.label)
			}
			return
		}
	}
	t.Error("no unidentified row; a bridge too old to identify itself would be " +
		"silently absent, which is the case the section exists to expose")
}

func TestProcRows_StaleBridgeIsFlagged(t *testing.T) {
	for _, r := range procModel().procRows() {
		if r.kind == procRowQuil && r.pid == 5044 {
			if r.flag == "" {
				t.Error("a bridge on an older version carries no stale flag")
			}
			return
		}
	}
	t.Fatal("bridge row missing")
}

// A report landing after the user left must not touch this dialog's state. The
// removed version wrote dialogCursor unconditionally, so a late scan moved the
// About cursor.
func TestApplyResourceReport_DoesNotTouchStateWhenAnotherDialogIsOpen(t *testing.T) {
	m := procModel()
	m.proc.cursor = 3

	m.dialog = dialogAbout
	m.dialogCursor = 7
	m = m.applyResourceReport(ipc.ResourceReportRespPayload{Total: 99})

	if m.dialogCursor != 7 {
		t.Errorf("dialogCursor moved to %d; a report arriving after Esc must not "+
			"move another dialog's cursor", m.dialogCursor)
	}
	if m.proc.cursor != 3 {
		t.Errorf("proc cursor moved to %d while the dialog was closed", m.proc.cursor)
	}
	// The status-bar total is the one thing that MUST still update, because it
	// is fed by this report whether or not the dialog is open.
	if m.lastResourceResp == nil || m.lastResourceResp.Total != 99 {
		t.Error("the status-bar total was not updated while another dialog was open")
	}
}

func TestRequestTrees_SingleFlightSuppressesThenTimesOut(t *testing.T) {
	m := procModel()
	now := time.Now()

	m, cmd := m.requestTrees(now)
	if cmd == nil {
		t.Fatal("first request produced no command")
	}

	// A second request while the first is in flight is suppressed.
	m, cmd = m.requestTrees(now.Add(time.Second))
	if cmd != nil {
		t.Error("a second request was issued while one was still in flight")
	}

	// ...but the suppression is BOUNDED. Without this the dialog would freeze
	// on one lost response, and the daemon's collector gate — renewed by these
	// same requests — would lapse with the dialog still open.
	m, cmd = m.requestTrees(now.Add(resourceRequestTimeout + time.Second))
	if cmd == nil {
		t.Error("single-flight never released; one lost response would freeze " +
			"refresh permanently and starve the collector gate")
	}
}

func TestProcTreesStale_OnlyPastTheThreshold(t *testing.T) {
	m := procModel()
	now := time.Now()

	m.proc.resp.TreesAt = now.Add(-time.Second).UnixNano()
	if m.procTreesStale(now) {
		t.Error("a one-second-old snapshot was reported stale")
	}

	m.proc.resp.TreesAt = now.Add(-2 * procStaleAfter).UnixNano()
	if !m.procTreesStale(now) {
		t.Error("an old snapshot was not reported stale; the dialog would present " +
			"frozen numbers as current")
	}
}

// The kill path, driven through Update rather than by calling the handler.
//
// This is the shape the removed version lacked entirely: its confirm branch was
// unreachable from the call site, so disabling it left the whole suite green.
func TestKillProcess_ThroughUpdate(t *testing.T) {
	m := procModel()

	// Put the cursor on the killable node.
	rows := m.procRows()
	for i, r := range rows {
		if r.pid == 5219 {
			m.proc.cursor = i
		}
	}

	updated, _ := m.handleProcessesDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = updated.(Model)

	if m.dialog != dialogConfirm || m.confirmKind != confirmKindKillProcess {
		t.Fatalf("K did not open the kill confirm (dialog=%v kind=%q)", m.dialog, m.confirmKind)
	}

	// The identity the user was shown must be what travels.
	payload := m.killRequestPayload()
	if payload.PID != 5219 {
		t.Errorf("PID = %d, want 5219", payload.PID)
	}
	if payload.PaneID != "pane-a" {
		t.Errorf("PaneID = %q, want pane-a", payload.PaneID)
	}
	if payload.StartMS == 0 {
		t.Error("StartMS is zero; the daemon refuses an unknown start time, so " +
			"the kill would always be rejected")
	}
	want, _ := rowWithPID(rows, 5219)
	if payload.StartMS != want.startMS {
		t.Errorf("StartMS = %d, want %d (the value the row displayed)",
			payload.StartMS, want.startMS)
	}
}

// Enter must not stop a process. It is the commit key everywhere else, and this
// confirm is reached from a list the user is scrolling.
func TestKillConfirm_RequiresExplicitY(t *testing.T) {
	m := procModel()
	rows := m.procRows()
	for i, r := range rows {
		if r.pid == 5219 {
			m.proc.cursor = i
		}
	}
	updated, _ := m.handleProcessesDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = updated.(Model)

	after, _ := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := after.(Model)
	if got.dialog != dialogConfirm {
		t.Errorf("Enter left the confirm (dialog=%v); only an explicit y may "+
			"stop a process", got.dialog)
	}
}

// Cancelling returns to the list, agreeing with the accept path. The removed
// version's Esc went to dialogNone while its accept path went elsewhere.
func TestKillConfirm_EscReturnsToProcesses(t *testing.T) {
	m := procModel()
	rows := m.procRows()
	for i, r := range rows {
		if r.pid == 5219 {
			m.proc.cursor = i
		}
	}
	updated, _ := m.handleProcessesDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = updated.(Model)

	after, _ := m.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := after.(Model)
	if got.dialog != dialogProcesses {
		t.Errorf("Esc from the kill confirm went to %v, want dialogProcesses", got.dialog)
	}
}

// A non-killable row must not open a confirm at all.
func TestKillProcess_RefusesPaneOwnChild(t *testing.T) {
	m := procModel()
	rows := m.procRows()
	for i, r := range rows {
		if r.pid == 4812 {
			m.proc.cursor = i
		}
	}
	updated, _ := m.handleProcessesDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = updated.(Model)

	if m.dialog == dialogConfirm {
		t.Error("K on the pane's own shell opened a kill confirm")
	}
	if m.proc.notice == "" {
		t.Error("K on a non-killable row said nothing; the key would look broken")
	}
}

// Lowercase k is cursor-up. Binding a destructive action to a navigation key is
// how a user kills a process while scrolling.
func TestProcessesDialog_LowercaseKMovesCursorNotKills(t *testing.T) {
	m := procModel()
	m.proc.cursor = 5

	updated, _ := m.handleProcessesDialogKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	got := updated.(Model)

	if got.dialog == dialogConfirm {
		t.Fatal("lowercase k opened the kill confirm")
	}
	if got.proc.cursor != 4 {
		t.Errorf("cursor = %d, want 4 (k is cursor-up)", got.proc.cursor)
	}
}

func TestApplyKillProcessResp_ShowsRefusalReason(t *testing.T) {
	m := procModel()
	m = m.applyKillProcessResp(ipc.KillProcessRespPayload{Refused: "that pane is no longer running"})

	if !strings.Contains(m.proc.notice, "no longer running") {
		t.Errorf("notice = %q, want the daemon's refusal reason — a refusal is a "+
			"normal outcome and must be visible", m.proc.notice)
	}
}

func TestProcTreeCPU_UnknownWhenNothingSampled(t *testing.T) {
	tree := &ipc.ProcNode{
		PID: 1, CPUPct: proctree.UnknownCPU,
		Children: []ipc.ProcNode{{PID: 2, CPUPct: proctree.UnknownCPU}},
	}
	if got := procTreeCPU(tree); got != proctree.UnknownCPU {
		t.Errorf("procTreeCPU = %v, want unknown — a pane whose processes have "+
			"not been sampled twice must not read as idle", got)
	}
}

func TestProcTreeCPU_SumsKnownValues(t *testing.T) {
	tree := &ipc.ProcNode{
		PID: 1, CPUPct: 2,
		Children: []ipc.ProcNode{
			{PID: 2, CPUPct: 36},
			{PID: 3, CPUPct: proctree.UnknownCPU},
		},
	}
	if got := procTreeCPU(tree); got != 38 {
		t.Errorf("procTreeCPU = %v, want 38 (unknown children contribute nothing)", got)
	}
}
