package tui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Everything in this file covers values that a well-behaved daemon never sends.
// Under --remote the daemon is a machine the user may not control, so the wire
// is a trust boundary and "our own code never produces this" is not a bound.

// Depth is a plain int off the socket and the indent is strings.Repeat, which
// PANICS on a negative count. Expanding a pane is the dialog's own happy path,
// so an unclamped negative depth is a deterministic remote-triggered client
// kill. Verified: depth -3 panics without the clamp.
func TestRenderProcRow_SurvivesHostileDepth(t *testing.T) {
	for _, depth := range []int{-1000, -3, -1, 0, 1, 50, 1 << 20} {
		t.Run(fmt.Sprintf("depth=%d", depth), func(t *testing.T) {
			row := procRow{kind: procRowProc, label: "node", pid: 42, depth: clampDepth(depth)}
			line := renderProcRow(row, false, 86)
			if w := lipgloss.Width(line); w > 86 {
				t.Errorf("row is %d cells against an 86-cell budget", w)
			}
		})
	}
}

func TestClampDepth(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-100, 1}, {-1, 1}, {0, 1}, {1, 1}, {5, 5},
		{maxProcIndentDepth, maxProcIndentDepth},
		{maxProcIndentDepth + 1, maxProcIndentDepth},
		{1 << 30, maxProcIndentDepth},
	} {
		if got := clampDepth(tc.in); got != tc.want {
			t.Errorf("clampDepth(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Numeric columns come off the wire too. A PID of MaxInt64 is 19 characters
// into a 7-cell column and an absurd CPU float formats to hundreds of them.
// Bounding only the strings leaves the row width dependent on values nobody
// validates — the 89-into-86 defect this dialog's predecessor was pulled for.
func TestRenderProcRow_BoundsHostileNumbers(t *testing.T) {
	rows := []procRow{
		{kind: procRowProc, label: "x", pid: math.MaxInt64, depth: 1, cpu: 1e308, rss: math.MaxUint64},
		{kind: procRowProc, label: "y", pid: math.MaxInt32, depth: 2, cpu: math.Inf(1), rss: 1 << 62},
		{kind: procRowProc, label: "z", pid: 1, depth: 1, cpu: math.NaN(), rss: 0},
		{
			kind: procRowQuil, label: "bridge", pid: math.MaxInt64,
			version: strings.Repeat("v", 200), exeName: strings.Repeat("e", 200),
		},
	}
	for i, row := range rows {
		for _, inner := range []int{86, 60, 40} {
			line := renderProcRow(row, false, inner)
			if w := lipgloss.Width(line); w > inner {
				t.Errorf("row %d at inner=%d is %d cells: %q", i, inner, w, line)
			}
		}
	}
}

// truncateToWidth measures in CELLS; fmt's %-*s pads in RUNES. A pane named
// 构建 is 2 runes and 4 cells, so padding by runes overshoots and the row soft
// wraps. sanitizeRemoteText preserves printable non-ASCII byte-identically, so
// remote process names arrive here unchanged — this is not a rare path, and
// every fixture in the original geometry test was ASCII.
func TestRenderProcRow_WideAndEmojiNamesStayWithinBudget(t *testing.T) {
	names := []string{
		"构建服务器进程名称很长很长很长",
		"🚀 api-server",
		"👨‍👩‍👧‍👦 family",
		"Ｆｕｌｌｗｉｄｔｈ",
		strings.Repeat("漢", 80),
		strings.Repeat("🔥", 60),
	}
	for _, name := range names {
		for _, inner := range []int{86, 60, 40, 20} {
			row := procRow{kind: procRowProc, label: name, pid: 4812, depth: 2, cpu: 12}
			line := renderProcRow(row, false, inner)
			if w := lipgloss.Width(line); w > inner {
				t.Errorf("name %q at inner=%d rendered %d cells (budget %d)",
					name, inner, w, inner)
			}
		}
	}
}

func TestPadCell_ExactCellWidth(t *testing.T) {
	for _, s := range []string{"", "a", "漢字", "🚀", "构建服务器", "abcdef"} {
		for _, w := range []int{1, 2, 5, 10} {
			if got := lipgloss.Width(padCell(s, w)); got != w {
				t.Errorf("padCell(%q, %d) is %d cells, want exactly %d", s, w, got, w)
			}
			if got := lipgloss.Width(padCellRight(s, w)); got != w {
				t.Errorf("padCellRight(%q, %d) is %d cells, want exactly %d", s, w, got, w)
			}
		}
	}
}

func TestProcWindow_KeepsCursorVisible(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		total, cursor, scroll, visible int
	}{
		{"cursor above window", 100, 5, 40, 10},
		{"cursor below window", 100, 90, 0, 10},
		{"cursor inside window", 100, 5, 0, 10},
		{"stale scroll past end", 10, 2, 500, 5},
		{"visible exceeds total", 3, 1, 0, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end := procWindow(tc.total, tc.cursor, tc.scroll, tc.visible)
			if start < 0 || end > tc.total || start > end {
				t.Fatalf("window [%d,%d) is out of range for total=%d", start, end, tc.total)
			}
			if tc.cursor < tc.total && (tc.cursor < start || tc.cursor >= end) {
				t.Errorf("cursor %d is outside the rendered window [%d,%d) — it "+
					"would move through rows nobody can see", tc.cursor, start, end)
			}
		})
	}
}

func TestProcWindow_EmptyList(t *testing.T) {
	if s, e := procWindow(0, 0, 0, 10); s != 0 || e != 0 {
		t.Errorf("empty list produced window [%d,%d)", s, e)
	}
}

// A treeless status-bar response must not replace the dialog's tree-bearing
// one. Both ride the same message, so a status-bar poll can still be in flight
// when the dialog opens; adopting it blanks the quil section and turns every
// CPU cell into an em dash for a round trip.
func TestApplyResourceReport_TreelessResponseDoesNotClobberTrees(t *testing.T) {
	m := procModel()
	before := len(m.procRows())

	m = m.applyResourceReport(ipc.ResourceReportRespPayload{Total: 123}) // WithTrees false

	if got := len(m.procRows()); got != before {
		t.Errorf("row count changed %d -> %d; a status-bar response replaced the "+
			"dialog's trees", before, got)
	}
	if m.proc.resp == nil || !m.proc.resp.WithTrees {
		t.Error("the stored response is no longer the tree-bearing one")
	}
	// The status-bar total still updates — it is fed by the same message.
	if m.lastResourceResp == nil || m.lastResourceResp.Total != 123 {
		t.Error("the status-bar total was not updated")
	}
}

func TestApplyKillProcessResp_IgnoredWhenDialogClosed(t *testing.T) {
	m := procModel()
	m.dialog = dialogNone

	m = m.applyKillProcessResp(ipc.KillProcessRespPayload{Refused: "nope"})

	if m.proc.notice != "" {
		t.Errorf("notice = %q; a response landing after Esc would surface the "+
			"next time the dialog opens", m.proc.notice)
	}
}

// The quil section's whole purpose: version, uptime and the binary name. A role
// and a PID answer neither "is this binary current" nor "how long has it been
// up", and a bridge pinning a renamed-aside binary is the observation that
// motivated the section.
func TestRenderProcRow_QuilRowShowsVersionUptimeAndBinary(t *testing.T) {
	row := procRow{
		kind: procRowQuil, label: "bridge", pid: 5044,
		version: "1.62.4", uptime: 51 * time.Hour, exeName: "quil.exe.old.3",
		flag: "⚠ stale",
	}
	line := renderProcRow(row, false, 86)

	for _, want := range []string{"bridge", "1.62.4", "5044", "quil.exe.old.3"} {
		if !strings.Contains(line, want) {
			t.Errorf("quil row is missing %q: %q", want, line)
		}
	}
	if !strings.Contains(line, "2d") {
		t.Errorf("uptime not rendered: %q", line)
	}
}

func TestFormatUptime(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "—"},
		{-time.Second, "—"},
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{2*time.Hour + 14*time.Minute, "2h 14m"},
		{6*24*time.Hour + 3*time.Hour, "6d 03h"},
	} {
		if got := formatUptime(tc.in); got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
