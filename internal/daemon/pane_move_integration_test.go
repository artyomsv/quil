//go:build integration

package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// moveAndRestart sends the exact move_pane payload the client sends, requires
// the handler to SCHEDULE a snapshot, writes it, and restores a fresh daemon
// from disk.
func moveAndRestart(t *testing.T, d *Daemon, paneID, tabID string) *Daemon {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgMovePane, ipc.MovePanePayload{PaneID: paneID, TabID: tabID})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	// snapshotCh is buffered to 1 and requestSnapshot is a non-blocking send,
	// so a pending request is exactly one queued item.
	if len(d.snapshotCh) != 1 {
		t.Error("the move handler scheduled no snapshot, so it lives only in " +
			"memory until the periodic ticker happens to fire")
	}

	d.snapshot()
	if _, err := persist.Load(config.WorkspacePath()); err != nil {
		t.Fatalf("Load workspace: %v", err)
	}
	fresh := New(config.Default())
	fresh.restoreWorkspace()
	return fresh
}

// The move has to survive the REAL round trip. It writes both sides of the
// link — the two tabs' Panes and the pane's own TabID — and restore builds
// panes from the tab loop, so a snapshot missing either side would bring the
// pane back in the wrong tab or listed twice.
func TestMovePane_SurvivesTheWireAndARestart(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	proj := d.session.CreateProject("alpha", "/home/a/alpha")
	src := d.session.CreateTabInProject(proj.ID, "src")
	dst := d.session.CreateTabInProject(proj.ID, "dst")
	moveTestPane(t, d, src.ID) // keeps src alive
	moving := moveTestPane(t, d, src.ID)
	moveTestPane(t, d, dst.ID)

	fresh := moveAndRestart(t, d, moving.ID, dst.ID)

	freshSrc, freshDst := fresh.session.Tab(src.ID), fresh.session.Tab(dst.ID)
	if freshSrc == nil || freshDst == nil {
		t.Fatalf("restored daemon lost a tab: src=%v dst=%v", freshSrc, freshDst)
	}
	if indexOfString(freshSrc.Panes, moving.ID) >= 0 {
		t.Errorf("restored src.Panes %v still lists the moved pane", freshSrc.Panes)
	}
	if n := len(freshDst.Panes); n == 0 || freshDst.Panes[n-1] != moving.ID {
		t.Errorf("restored dst.Panes = %v, want it ending with the moved pane", freshDst.Panes)
	}
	restored := fresh.session.Pane(moving.ID)
	if restored == nil {
		t.Fatalf("restored daemon has no pane %s", moving.ID)
	}
	if got := restored.CurrentTabID(); got != dst.ID {
		t.Errorf("restored pane.TabID = %q, want %q", got, dst.ID)
	}
}

func TestMovePane_DissolvedTabDoesNotComeBack(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	proj := d.session.CreateProject("alpha", "/home/a/alpha")
	src := d.session.CreateTabInProject(proj.ID, "src")
	dst := d.session.CreateTabInProject(proj.ID, "dst")
	only := moveTestPane(t, d, src.ID)
	moveTestPane(t, d, dst.ID)

	fresh := moveAndRestart(t, d, only.ID, dst.ID)

	if fresh.session.Tab(src.ID) != nil {
		t.Error("the dissolved source tab came back after a restart")
	}
	if p, ok := projectByID(fresh, proj.ID); !ok || indexOfString(p.TabIDs, src.ID) >= 0 {
		t.Errorf("restored project %+v still lists the dissolved tab", p)
	}
	if restored := fresh.session.Pane(only.ID); restored == nil || restored.CurrentTabID() != dst.ID {
		t.Errorf("the moved pane did not come back in the target tab")
	}
}
