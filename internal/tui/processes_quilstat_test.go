package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/proctree"
)

// The QUIL section used to carry no resource columns at all, so the dialog
// could show a pane's claude burning CPU while saying nothing about the TUI
// that was burning more than any of them. These pin that it now can — and that
// it still refuses to invent a number when it has none.

func quilRow(cpu float64, rss uint64, age time.Duration) procRow {
	return procRow{
		kind:    procRowQuil,
		label:   "tui",
		pid:     15304,
		version: "1.63.5",
		uptime:  45 * time.Hour,
		exeName: "quil.exe",
		cpu:     cpu,
		rss:     rss,
		statAge: age,
	}
}

func TestRenderProcRow_QuilRowShowsCPUAndMemory(t *testing.T) {
	line := renderProcRow(quilRow(11.5, 703<<20, time.Second), false, 120)

	if !strings.Contains(line, "12%") {
		t.Errorf("quil row does not show the CPU percentage: %q", line)
	}
	if !strings.Contains(line, "MB") {
		t.Errorf("quil row does not show memory: %q", line)
	}
}

// The same convention the pane rows already keep, now reaching the section
// where the hot process actually lives.
func TestRenderProcRow_QuilRowUnknownCPUIsEmDashNotZero(t *testing.T) {
	line := renderProcRow(quilRow(proctree.UnknownCPU, 703<<20, time.Second), false, 120)

	if strings.Contains(line, "0%") {
		t.Errorf("unreported CPU rendered as 0%%: %q — that reads as idle", line)
	}
	if !strings.Contains(line, "—") {
		t.Errorf("unreported CPU did not render an em dash: %q", line)
	}
}

// Zero bytes is the unknown marker for RSS: a live process cannot occupy zero,
// so "0 B" would be a claim rather than a measurement.
func TestRenderProcRow_QuilRowZeroRSSIsEmDashNotZeroBytes(t *testing.T) {
	line := renderProcRow(quilRow(11.5, 0, time.Second), false, 120)

	if strings.Contains(line, "0 B") {
		t.Errorf("unreported RSS rendered as \"0 B\": %q", line)
	}
	if !strings.Contains(line, "—") {
		t.Errorf("unreported RSS did not render an em dash: %q", line)
	}
}

// A process that stopped reporting is the interesting case, not a rounding
// detail: a wedged TUI keeps its connection open, so its last reading would sit
// on screen looking current for as long as the dialog stayed open.
func TestRenderProcRow_QuilRowStaleStatIsEmDash(t *testing.T) {
	line := renderProcRow(quilRow(11.5, 703<<20, procStatStaleAfter+time.Second), false, 120)

	if strings.Contains(line, "12%") {
		t.Errorf("a stale reading still rendered as a live percentage: %q", line)
	}
	if !strings.Contains(line, "—") {
		t.Errorf("stale reading did not render an em dash: %q", line)
	}
}

func TestRenderProcRow_QuilRowFreshStatIsNotStale(t *testing.T) {
	line := renderProcRow(quilRow(11.5, 703<<20, procStatStaleAfter-time.Second), false, 120)

	if !strings.Contains(line, "12%") {
		t.Errorf("a fresh reading was treated as stale: %q", line)
	}
}

// A row for a process that has NEVER reported carries a zero age, which must
// not be read as "reported zero milliseconds ago" and therefore fresh. The CPU
// value is what settles it, and it is unknown in that case.
func TestRenderProcRow_QuilRowNeverReportedIsEmDash(t *testing.T) {
	line := renderProcRow(quilRow(proctree.UnknownCPU, 0, 0), false, 120)

	if strings.Contains(line, "0%") || strings.Contains(line, "0 B") {
		t.Errorf("a never-reported process rendered as zeroes: %q", line)
	}
}

// The wire values have to actually reach the row builder. A renderer that works
// against a hand-built procRow proves nothing if procRows never fills it.
func TestProcRows_QuilRowsCarryReportedStat(t *testing.T) {
	m := Model{lastWidth: 200}
	m = m.openProcessesDialog()
	m = m.applyResourceReport(ipc.ResourceReportRespPayload{
		WithTrees: true,
		Quil: []ipc.QuilProcInfo{{
			Role: "tui", PID: 15304, Version: "1.63.5", ExeName: "quil.exe",
			UptimeMS: 1000, CPUPct: 11.5, RSSBytes: 703 << 20, StatAgeMS: 1200,
		}},
	})

	var got *procRow
	for i, r := range m.procRows() {
		if r.kind == procRowQuil && r.pid == 15304 {
			got = &m.procRows()[i]
			break
		}
	}
	if got == nil {
		t.Fatal("no quil row for pid 15304")
	}
	if got.cpu != 11.5 {
		t.Errorf("row cpu = %v, want 11.5", got.cpu)
	}
	if got.rss != 703<<20 {
		t.Errorf("row rss = %d, want %d", got.rss, uint64(703)<<20)
	}
	if got.statAge != 1200*time.Millisecond {
		t.Errorf("row statAge = %v, want 1.2s", got.statAge)
	}
}

// Every row in the section must still fit the budget now that two columns were
// added to it — the failure this file's neighbours exist to catch.
func TestRenderProcRow_QuilRowFitsNarrowBudgets(t *testing.T) {
	for _, inner := range []int{40, 60, 80, 100, 120} {
		line := renderProcRow(quilRow(11.5, 703<<20, time.Second), false, inner)
		if w := lipgloss.Width(line); w > inner {
			t.Errorf("inner=%d produced a %d-cell row: %q", inner, w, line)
		}
	}
}

// Fitting the budget is not enough: the binary column has to survive it.
//
// "quil.exe.old.3" is the string this whole section exists to surface — a
// bridge still executing a binary an in-place update renamed aside. Adding the
// MEM and CPU columns is exactly the kind of change that squeezes it out, and
// the overflow test above cannot see that happen because a truncated row is
// still a correctly-sized row.
func TestQuilRow_BinaryColumnSurvivesAnEightyColumnTerminal(t *testing.T) {
	const renamed = "quil.exe.old.3"

	// An 80-column terminal: the dialog clamps to 78, leaving 72 inner cells.
	for _, inner := range []int{72, dialogInnerWidth(200, processesDialogWidth)} {
		line := renderProcRow(procRow{
			kind:    procRowQuil,
			label:   "bridge",
			pid:     56356,
			version: "1.63.4",
			uptime:  45 * time.Hour,
			exeName: renamed,
			cpu:     11.5,
			rss:     703 << 20,
			statAge: time.Second,
		}, false, inner)

		if !strings.Contains(line, renamed) {
			t.Errorf("inner=%d truncated the binary name: %q\n"+
				"a renamed binary is the one thing this section must always show",
				inner, line)
		}
	}
}

// The role column has to hold its own widest line, or the stale marker and the
// role collide. Measured in CELLS, because the marker is non-ASCII.
func TestQuilNameCol_HoldsTheWidestRoleLine(t *testing.T) {
	// The shape renderProcRow builds for a stale process.
	widest := "  ⚠ stale " + "daemon"
	if w := lipgloss.Width(widest); w > quilNameCol {
		t.Errorf("widest role line %q is %d cells, exceeding quilNameCol=%d",
			widest, w, quilNameCol)
	}
}
