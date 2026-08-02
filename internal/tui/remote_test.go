package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestModel_RemoteMode_ReflectsDest(t *testing.T) {
	var m Model

	if m.RemoteMode() {
		t.Error("RemoteMode() = true on a zero Model, want false")
	}

	m.SetRemoteDest("gpu01")
	if !m.RemoteMode() {
		t.Error("RemoteMode() = false after SetRemoteDest, want true")
	}
	if m.remoteDest != "gpu01" {
		t.Errorf("remoteDest = %q, want %q", m.remoteDest, "gpu01")
	}

	m.SetRemoteDest("")
	if m.RemoteMode() {
		t.Error("RemoteMode() = true after clearing the destination, want false")
	}
}

// TestMaybeShowUpdateNotice_SuppressedInRemoteMode pins the cross-host guard.
// m.updateInfo is broadcast by the REMOTE daemon and describes its staging
// directory, but accepting the notice applies a LOCAL staged update — so the
// dialog offers an action wired to the wrong machine. Showing it would also
// write the remote's version into this machine's notified-version marker,
// suppressing the genuine local notice for that version.
func TestMaybeShowUpdateNotice_SuppressedInRemoteMode(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}}
	m.SetRemoteDest("gpu01")

	m.maybeShowUpdateNotice()

	if m.dialog == dialogUpdateNotice {
		t.Error("update notice shown in remote mode, want suppressed")
	}
}

// TestMaybeShowUpdateNotice_StillShownLocally is the control for the test
// above: the suppression must be conditional, not a permanent disable.
func TestMaybeShowUpdateNotice_StillShownLocally(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}}

	m.maybeShowUpdateNotice()

	if m.dialog != dialogUpdateNotice {
		t.Errorf("dialog = %v in a local session, want dialogUpdateNotice", m.dialog)
	}
}

// TestRemoteModeFollowsActiveProject pins the per-project half of RemoteMode:
// with no remoteDest set (today's --remote session's own signal, still
// honoured — see TestModel_RemoteMode_ReflectsDest), the answer must track
// whichever project is active, not a single process-wide flag.
func TestRemoteModeFollowsActiveProject(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"},
	}}

	m.activeProject = 0
	if m.RemoteMode() {
		t.Fatal("a local project must not report remote mode")
	}
	m.activeProject = 1
	if !m.RemoteMode() {
		t.Fatal("a project on gpu01 must report remote mode")
	}
}

// TestStatusBarNamesTheActiveProjectsHost pins the status-bar half: the
// [remote …] segment must name the ACTIVE project's own host, not only the
// session-wide remoteDest.
func TestStatusBarNamesTheActiveProjectsHost(t *testing.T) {
	m := Model{
		projects:      []*ProjectModel{{ID: "proj-gpu", Dest: "gpu01"}},
		activeProject: 0, width: 120,
		// renderStatusBar dereferences this unconditionally (notification
		// count badge) — every other caller in this package sets it too.
		notifications: NewNotificationCenter(30, 50),
	}
	if !strings.Contains(m.renderStatusBar(), "gpu01") {
		t.Fatal("the status bar must name the host the user is actually on")
	}
}

// TestAttachCWDIsEmptyForRemoteProject pins attachCWD's existing per-dest
// contract — unchanged by this task, but worth pinning here alongside its
// two new neighbours since a future per-dest attach (Task 17) call site will
// depend on it just the same.
func TestAttachCWDIsEmptyForRemoteProject(t *testing.T) {
	if got := attachCWD("gpu01", "/home/me/src"); got != "" {
		t.Fatalf("attachCWD = %q, want empty — the laptop's path is not the "+
			"remote machine's, and defaultCWD() falls back safely", got)
	}
}
