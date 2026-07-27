package tui

import (
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
