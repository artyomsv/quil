package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// procModelWithTabs builds a report with n tabs, each holding one pane, so the
// row count exceeds any plausible terminal height.
//
// The 33-tab / 36-pane workspace this repo records in .claude/CLAUDE.md is the
// real shape being defended: with nothing expanded that alone is ~42 rows.
func procModelWithTabs(n int) Model {
	resp := ipc.ResourceReportRespPayload{
		WithTrees:    true,
		CPUSupported: true,
		Total:        1 << 30,
		Quil: []ipc.QuilProcInfo{
			{Role: "tui", PID: 1, Version: "1.62.6", ExeName: "quil"},
			{Role: "daemon", PID: 2, Version: "1.62.6", ExeName: "quild"},
		},
	}
	for i := 0; i < n; i++ {
		resp.Panes = append(resp.Panes, ipc.PaneResourceInfo{
			PaneID:     fmt.Sprintf("pane-%d", i),
			TabID:      fmt.Sprintf("tab-%d", i),
			TotalBytes: uint64(i+1) << 20,
			Tree: &ipc.ProcNode{
				PID: 1000 + i, Name: "zsh", Depth: 1,
				Children: []ipc.ProcNode{{PID: 2000 + i, Name: "node", Depth: 2}},
			},
		})
	}

	m := Model{lastWidth: 200, height: 40}
	m = m.openProcessesDialog()
	m = m.applyResourceReport(resp)
	return m
}

// renderDialog places the box with lipgloss.Place, which this repo documents in
// six places as NOT clipping. Without a row window the dialog draws past the
// bottom of the terminal, and what falls off is the footer — the line telling
// the user which keys work — plus every row below the fold, which the cursor
// can still be moved into.
func TestRenderProcessesDialog_FitsTerminalHeight(t *testing.T) {
	for _, h := range []int{60, 40, 24, 12, 8} {
		t.Run(fmt.Sprintf("h=%d", h), func(t *testing.T) {
			m := procModelWithTabs(40)
			m.height, m.lastWidth = h, 200

			lines := strings.Count(m.renderProcessesDialog(), "\n") + 1
			if lines > h {
				t.Errorf("dialog rendered %d lines into a %d-row terminal — the "+
					"footer and everything below the fold are off-screen", lines, h)
			}
		})
	}
}

// Moving to the end of a long list must keep the cursor on a rendered row.
func TestProcessesDialog_CursorStaysWithinRenderedRows(t *testing.T) {
	m := procModelWithTabs(40)
	m.height = 24

	rows := m.procRows()
	m.proc.cursor = len(rows) - 1
	m = m.syncProcScroll(len(rows))

	start, end := procWindow(len(rows), m.proc.cursor, m.proc.scroll, m.procVisibleRows())
	if m.proc.cursor < start || m.proc.cursor >= end {
		t.Errorf("cursor %d sits outside the rendered window [%d,%d)",
			m.proc.cursor, start, end)
	}
}

// The list says what is off screen rather than ending silently — a workspace
// with more panes than rows would otherwise look like it has fewer.
func TestRenderProcessesDialog_ReportsHiddenRows(t *testing.T) {
	m := procModelWithTabs(40)
	m.height, m.lastWidth = 20, 200

	out := m.renderProcessesDialog()
	if !strings.Contains(out, "below") {
		t.Errorf("no indication that rows are off screen:\n%s", out)
	}
}
