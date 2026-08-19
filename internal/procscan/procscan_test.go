package procscan

import (
	"testing"
	"time"
)

var base = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func TestOrphans_LiveParentIsNotAnOrphan(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 100, PPID: 1, Name: "claude.exe", Cmdline: "claude", Start: base},
		{PID: 200, PPID: 100, Name: "quil.exe", Cmdline: "quil mcp", Start: base.Add(time.Second)},
	}
	if got := Orphans(procs, 999); len(got) != 0 {
		t.Errorf("classified %d orphans, want 0 — the bridge's parent is in the snapshot", len(got))
	}
}

func TestOrphans_MissingParentIsAnOrphan(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 200, PPID: 100, Name: "quil.exe", Cmdline: "quil mcp", Start: base},
	}
	got := Orphans(procs, 999)
	if len(got) != 1 || got[0].PID != 200 {
		t.Fatalf("got %+v, want the bridge at PID 200 classified as an orphan", got)
	}
}

// PID reuse: a "parent" that started AFTER its supposed child cannot be one.
func TestOrphans_ParentYoungerThanChildIsNotAParent(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 100, PPID: 1, Name: "impostor.exe", Cmdline: "impostor", Start: base.Add(time.Hour)},
		{PID: 200, PPID: 100, Name: "quil.exe", Cmdline: "quil mcp", Start: base},
	}
	if got := Orphans(procs, 999); len(got) != 1 {
		t.Errorf("got %d orphans, want 1 — PID 100 is younger than its supposed child, so it is a reused PID", len(got))
	}
}

// An unknown start time must count as LIVE. A false orphan invites the user to
// kill something still in use; a missed one merely goes unoffered.
func TestOrphans_UnknownStartTimeCountsAsLive(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 100, PPID: 1, Cmdline: "claude"}, // Start zero: OpenProcess refused
		{PID: 200, PPID: 100, Cmdline: "quil mcp", Start: base},
	}
	if got := Orphans(procs, 999); len(got) != 0 {
		t.Errorf("got %d orphans — a parent whose start time is unknown must not be "+
			"treated as dead, or the dialog offers to kill a live session's bridge", len(got))
	}
}

func TestOrphans_NeverClassifiesTheCallerItself(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 200, PPID: 100, Cmdline: "quil mcp", Start: base},
	}
	if got := Orphans(procs, 200); len(got) != 0 {
		t.Error("the calling process must never be offered for killing")
	}
}

// The npx churn that prompted this feature is parentless and NOT ours. It is
// worth reporting, but quil must not offer to terminate it.
func TestOrphans_IgnoresNonQuilProcesses(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 300, PPID: 999999, Name: "node.exe", Cmdline: "npx -y @upstash/context7-mcp"},
	}
	if got := Orphans(procs, 1); len(got) != 0 {
		t.Error("a parentless process that is not ours must be reported, not offered for killing")
	}
}

// The update swap renames the running binary aside, so a live bridge can be
// executing quil.exe.old.3. Matching on the image name would miss it — observed
// in production, a bridge from two days earlier pinning an old binary.
func TestIsBridge_MatchesARenamedBinary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cmdline string
		want    bool
	}{
		{"quil mcp", true},
		{`C:\Tools\quil\quil.exe mcp`, true},
		{`C:\Tools\quil\quil.exe.old.3 mcp`, true},
		{"/usr/local/bin/quil mcp", true},
		{"quil", false},
		{"quil notify setup", false},
		{"quild --background", false},
		{"claude mcp", false}, // not our binary
		{"", false},
	}
	for _, tc := range cases {
		if got := (Process{Cmdline: tc.cmdline}).IsBridge(); got != tc.want {
			t.Errorf("IsBridge(%q) = %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

func TestClassify_AssignsEachKind(t *testing.T) {
	t.Parallel()
	procs := []Process{
		{PID: 10, PPID: 1, Cmdline: "quil", Start: base},
		{PID: 11, PPID: 10, Cmdline: "quild --background", Start: base},
		{PID: 12, PPID: 1, Cmdline: "claude", Start: base},
		{PID: 13, PPID: 12, Cmdline: "quil mcp", Start: base},
		{PID: 14, PPID: 9999, Cmdline: "quil mcp", Start: base},
		{PID: 15, PPID: 1, Cmdline: "node server.js", Start: base},
	}
	kinds := Classify(procs, 1)
	want := map[int]Kind{
		10: KindTUI,
		11: KindDaemon,
		12: KindOther,
		13: KindBridge,
		14: KindOrphanBridge,
		15: KindOther,
	}
	for pid, w := range want {
		if kinds[pid] != w {
			t.Errorf("pid %d classified %v, want %v", pid, kinds[pid], w)
		}
	}
}

// quild must never be classified as the TUI, whatever the prefix match does.
func TestIsDaemon_DoesNotCollideWithTheTUI(t *testing.T) {
	t.Parallel()
	if !(Process{Cmdline: "quild --background"}).IsDaemon() {
		t.Error("quild --background is the daemon")
	}
	if !(Process{Cmdline: `E:\Tools\quil\quild-dev.exe --background`}).IsDaemon() {
		t.Error("quild-dev is the daemon")
	}
	if (Process{Cmdline: "quil"}).IsDaemon() {
		t.Error("quil is not the daemon")
	}
	if isQuilTUI(Process{Cmdline: "quild --background"}) {
		t.Error("quild must not classify as the TUI")
	}
}
