package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/procscan"
)

func procRows() []processRow {
	base := time.Now().Add(-time.Hour)
	return []processRow{
		{proc: procscan.Process{PID: 14, Name: "quil.exe", Cmdline: "quil mcp", Start: base}, kind: procscan.KindOrphanBridge},
		{proc: procscan.Process{PID: 10, Name: "quil.exe", Cmdline: "quil", Start: base}, kind: procscan.KindTUI},
		{proc: procscan.Process{PID: 11, Name: "quild.exe", Cmdline: "quild --background", Start: base}, kind: procscan.KindDaemon},
		{proc: procscan.Process{PID: 13, Name: "quil.exe", Cmdline: "quil mcp", Start: base}, kind: procscan.KindBridge},
	}
}

// Only an orphaned bridge may be killed. A live bridge, the daemon and the TUI
// itself must all be inert on Enter — killing any of them from a diagnostic is
// exactly the accident this dialog must not enable.
func TestProcessRow_OnlyOrphanBridgesAreKillable(t *testing.T) {
	t.Parallel()
	for _, r := range procRows() {
		want := r.kind == procscan.KindOrphanBridge
		if got := r.killable(); got != want {
			t.Errorf("kind %v killable = %v, want %v", r.kind, got, want)
		}
	}
}

// Enter on a non-killable row must not open a confirm. A confirm that appears
// and then refuses is worse than one that never appears: the user has already
// decided by the time they read it.
func TestHandleProcessesKey_EnterOnALiveBridgeOpensNoConfirm(t *testing.T) {
	t.Parallel()
	m := Model{dialog: dialogProcesses, processes: processesState{rows: procRows(), scanned: time.Now()}}
	m.dialogCursor = 1 // the TUI row

	updated, _ := m.handleProcessesKey(keyPressFor("enter"))
	got := updated.(Model)
	if got.dialog == dialogConfirm {
		t.Fatal("Enter on a non-orphan opened a kill confirm")
	}
	if got.flashText == "" {
		t.Error("Enter on a non-orphan should explain why nothing happened")
	}
}

func TestHandleProcessesKey_EnterOnAnOrphanOpensTheConfirm(t *testing.T) {
	t.Parallel()
	m := Model{dialog: dialogProcesses, processes: processesState{rows: procRows(), scanned: time.Now()}}
	m.dialogCursor = 0 // the orphan row

	updated, _ := m.handleProcessesKey(keyPressFor("enter"))
	got := updated.(Model)
	if got.dialog != dialogConfirm {
		t.Fatalf("dialog = %v, want dialogConfirm", got.dialog)
	}
	if got.confirmKind != confirmKindKillProcess {
		t.Errorf("confirmKind = %q, want %q", got.confirmKind, confirmKindKillProcess)
	}
	if got.confirmID != "14" {
		t.Errorf("confirmID = %q, want the orphan's pid \"14\"", got.confirmID)
	}
}

// An empty list must not panic on Enter, and the cursor must not run off either
// end of a list that shrank under it after a rescan.
func TestHandleProcessesKey_BoundsAreSafe(t *testing.T) {
	t.Parallel()
	m := Model{dialog: dialogProcesses}
	if _, cmd := m.handleProcessesKey(keyPressFor("enter")); cmd != nil {
		t.Error("Enter on an empty list should do nothing")
	}

	m.processes = processesState{rows: procRows(), scanned: time.Now()}
	m.dialogCursor = 0
	for i := 0; i < 10; i++ {
		updated, _ := m.handleProcessesKey(keyPressFor("up"))
		m = updated.(Model)
	}
	if m.dialogCursor != 0 {
		t.Errorf("cursor ran below zero: %d", m.dialogCursor)
	}
	for i := 0; i < 20; i++ {
		updated, _ := m.handleProcessesKey(keyPressFor("down"))
		m = updated.(Model)
	}
	if m.dialogCursor != len(m.processes.rows)-1 {
		t.Errorf("cursor = %d, want the last row %d", m.dialogCursor, len(m.processes.rows)-1)
	}
}

// A rescan returning fewer rows must not leave the cursor past the end.
func TestApplyProcessesScan_ClampsAStaleCursor(t *testing.T) {
	t.Parallel()
	m := Model{dialog: dialogProcesses, processes: processesState{rows: procRows()}}
	m.dialogCursor = 3

	m = m.applyProcessesScan(processesScannedMsg{rows: procRows()[:1]})
	if m.dialogCursor != 0 {
		t.Errorf("cursor = %d after the list shrank to 1 row, want 0", m.dialogCursor)
	}
}

// The three states the dialog can be in must each say something. A blank box is
// indistinguishable from a hung scan.
func TestRenderProcessesDialog_EveryStateSaysSomething(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state processesState
		want  string
	}{
		{"never scanned", processesState{}, "scanning"},
		{"error", processesState{err: errFake, scanned: time.Now()}, "boom"},
		{"empty", processesState{scanned: time.Now()}, "no quil processes"},
		{"rows", processesState{rows: procRows(), scanned: time.Now()}, "orphaned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{dialog: dialogProcesses, processes: tc.state}
			out := m.renderProcessesDialog()
			if !strings.Contains(strings.ToLower(out), tc.want) {
				t.Errorf("render for %s does not mention %q:\n%s", tc.name, tc.want, out)
			}
		})
	}
}

// The path is elided in the MIDDLE. A bridge running quil.exe.old.3 is exactly
// what this dialog exists to surface, and truncating the end hides it.
func TestProcessRowText_KeepsTheTailOfALongPath(t *testing.T) {
	t.Parallel()
	r := processRow{
		kind: procscan.KindOrphanBridge,
		proc: procscan.Process{
			PID:     15656,
			Cmdline: `C:\Users\someone\very\deep\path\to\Tools\quil\quil.exe.old.3 mcp`,
			Start:   time.Now().Add(-48 * time.Hour),
		},
	}
	got := processRowText(r)
	if !strings.Contains(got, "old.3") {
		t.Errorf("row dropped the tail of the path, which is the informative half:\n%s", got)
	}
	if !strings.Contains(got, "15656") {
		t.Errorf("row dropped the pid:\n%s", got)
	}
}

type fakeErr struct{}

func (fakeErr) Error() string { return "boom" }

var errFake = fakeErr{}
