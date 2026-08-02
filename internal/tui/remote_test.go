package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/update"
)

func TestModel_RemoteMode_ReflectsTheActiveProjectsDest(t *testing.T) {
	var m Model

	if m.RemoteMode() {
		t.Error("RemoteMode() = true on a zero Model, want false")
	}

	m.asRemote("gpu01")
	if !m.RemoteMode() {
		t.Error("RemoteMode() = false with a remote project active, want true")
	}

	m.asRemote("")
	if m.RemoteMode() {
		t.Error("RemoteMode() = true with a LOCAL project active, want false")
	}
}

// TestMaybeShowUpdateNotice_SuppressedInRemoteMode pins the cross-host guard.
// The announcement is broadcast by the REMOTE daemon and describes its staging
// directory, but accepting the notice applies a LOCAL staged update — so the
// dialog offers an action wired to the wrong machine. Showing it would also
// write the remote's version into this machine's notified-version marker,
// suppressing the genuine local notice for that version.
func TestMaybeShowUpdateNotice_SuppressedInRemoteMode(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1", updateInfos: map[string]*ipc.UpdateInfo{
		"gpu01": {LatestVersion: "0.0.2", InstallWritable: true},
	}}
	m.asRemote("gpu01")

	m.maybeShowUpdateNotice("gpu01")

	if m.dialog == dialogUpdateNotice {
		t.Error("update notice shown in remote mode, want suppressed")
	}
}

// TestMaybeShowUpdateNotice_StillShownLocally is the control for the test
// above: the suppression must be conditional, not a permanent disable.
func TestMaybeShowUpdateNotice_StillShownLocally(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1", updateInfos: map[string]*ipc.UpdateInfo{
		"": {LatestVersion: "0.0.2", InstallWritable: true},
	}}

	m.maybeShowUpdateNotice("")

	if m.dialog != dialogUpdateNotice {
		t.Errorf("dialog = %v in a local session, want dialogUpdateNotice", m.dialog)
	}
}

// TestNoteWorkspaceState_RemoteFirstBroadcast_NoNoticeNoMarker is the
// regression test for the window this guard used to be DEAD in.
//
// noteWorkspaceState runs BEFORE applyWorkspaceState, so on the very first
// broadcast of a `--remote` session m.projects is still nil: activeDest()
// answers "" and RemoteMode() answers false, for a session that is entirely
// remote. Gating on the broadcast's own Dest is what makes the guard live at
// the only moment it matters.
//
// Two things must not happen: the local update dialog must not open, and
// SaveNotifiedVersion must not write the REMOTE's version into this machine's
// marker — that write is the permanent half, since it suppresses the genuine
// local notice for that version forever after.
func TestNoteWorkspaceState_RemoteFirstBroadcast_NoNoticeNoMarker(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	// No projects at all — exactly the pre-applyWorkspaceState state.
	m := &Model{version: "0.0.1"}
	if m.RemoteMode() {
		t.Fatal("setup invariant broken: a Model with no projects must read local, " +
			"which is precisely why RemoteMode() cannot be the guard here")
	}

	m.noteWorkspaceState(&ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}, "gpu01")

	if m.dialog == dialogUpdateNotice {
		t.Error("the remote daemon's announcement opened the LOCAL update notice")
	}
	if got := update.LoadNotifiedVersion(config.UpdateNotifiedPath()); got != "" {
		t.Errorf("notified-version marker = %q, want empty — the remote's version was "+
			"written to this machine's marker and will suppress the real local notice", got)
	}
	// The gate must also be untouched, or whichever daemon broadcasts first
	// costs the local one its once-per-launch notice.
	if m.sawFirstState {
		t.Error("a remote broadcast consumed the once-per-launch notice gate")
	}

	// Control: the LOCAL daemon's own first broadcast still offers it.
	m.noteWorkspaceState(&ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}, "")
	if m.dialog != dialogUpdateNotice {
		t.Errorf("dialog = %v after the local broadcast, want dialogUpdateNotice", m.dialog)
	}
}

// TestStatusBarUpdateSegmentDescribesTheActiveDaemon pins the other half of the
// per-destination announcement table: with a LOCAL project active the segment
// must describe the LOCAL daemon, whatever the remote announced most recently.
// A single last-broadcast field passes the !RemoteMode() gate and then renders
// the remote host's version.
func TestStatusBarUpdateSegmentDescribesTheActiveDaemon(t *testing.T) {
	m := Model{
		version: "1.0.0", width: 200,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"},
		},
		activeProject: 0,
		updateInfos: map[string]*ipc.UpdateInfo{
			"":      nil,
			"gpu01": {LatestVersion: "9.9.9", InstallWritable: true},
		},
		notifications: NewNotificationCenter(30, 50),
	}
	if got := m.renderStatusBar(); strings.Contains(got, "9.9.9") {
		t.Errorf("the status bar offered the REMOTE daemon's update while a local "+
			"project is active:\n%s", got)
	}

	// Control: the local daemon's own announcement still renders.
	m.updateInfos[""] = &ipc.UpdateInfo{LatestVersion: "1.1.0", InstallWritable: true}
	if got := m.renderStatusBar(); !strings.Contains(got, "1.1.0") {
		t.Errorf("the local daemon's update segment is missing:\n%s", got)
	}
}

// TestRemoteModeFollowsActiveProject pins the whole of RemoteMode: the answer
// tracks whichever project is ACTIVE, and there is no process-wide flag left
// beside it.
//
// This is the case the deleted session-wide flag could not express, and it is
// what every caller depends on. With a mixed client, RemoteMode() gates update
// controls that are wired to LOCAL disk and plugin availability that describes
// one specific machine — so answering "remote" for a local project points both
// at the wrong host.
func TestRemoteModeFollowsActiveProject(t *testing.T) {
	m := Model{projects: []*ProjectModel{
		{ID: "proj-local", Dest: ""}, {ID: "proj-gpu", Dest: "gpu01"},
	}}

	m.activeProject = 0
	if m.RemoteMode() {
		t.Error("RemoteMode() = true while a LOCAL project is active; the update " +
			"controls it suppresses are wired to this machine's disk and would " +
			"stay hidden for a daemon that is on it")
	}
	m.activeProject = 1
	if !m.RemoteMode() {
		t.Error("RemoteMode() = false with the remote project active")
	}
}

// TestStatusBarNamesTheActiveProjectsHost pins the status-bar half: the
// [remote …] segment must name the ACTIVE project's own host. In a mixed
// session that is the only correct answer — the badge says which MACHINE the
// panes on screen are running on, and these panes routinely run AI agents with
// permission prompts disabled.
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
