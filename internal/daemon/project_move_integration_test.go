//go:build integration

package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// The move has to survive the REAL round trip: encode → handleMessage →
// snapshot → restore. The unit tests call SessionManager.MoveTab directly, so
// they say nothing about whether the handler is wired up, and it reassigns
// BOTH the tab's own ProjectID and the two projects' TabIDs/ActiveTab — a
// snapshot that missed any one of those four fields would bring the tab back
// on the wrong side of the link, or highlighted in a project whose visible
// list no longer contains it.
func TestMoveTab_SurvivesTheWireAndARestart(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	src := d.session.CreateProject("alpha", "/home/a/alpha")
	dst := d.session.CreateProject("beta", "/home/a/beta")
	// A second tab in src so the move is an ordinary reassignment rather than
	// also exercising the empty-project recovery path.
	d.session.CreateTabInProject(src.ID, "staying")
	tab := d.session.CreateTabInProject(src.ID, "moving")

	// The exact payload the client sends.
	msg, err := ipc.NewMessage(ipc.MsgMoveTab, ipc.MoveTabPayload{
		TabID:     tab.ID,
		ProjectID: dst.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	// The handler must SCHEDULE the write, not merely leave it in memory.
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

	// A fresh daemon must agree on BOTH sides of the link.
	fresh := New(config.Default())
	fresh.restoreWorkspace()

	freshSrc, ok := projectByID(fresh, src.ID)
	if !ok {
		t.Fatalf("restored daemon has no project %s", src.ID)
	}
	freshDst, ok := projectByID(fresh, dst.ID)
	if !ok {
		t.Fatalf("restored daemon has no project %s", dst.ID)
	}

	if indexOfString(freshSrc.TabIDs, tab.ID) >= 0 {
		t.Errorf("restored src.TabIDs %v still lists the moved tab", freshSrc.TabIDs)
	}
	if indexOfString(freshDst.TabIDs, tab.ID) < 0 {
		t.Errorf("restored dst.TabIDs %v does not list the moved tab", freshDst.TabIDs)
	}
	if freshDst.ActiveTab != tab.ID {
		t.Errorf("restored dst.ActiveTab = %q, want the moved tab %q", freshDst.ActiveTab, tab.ID)
	}

	restoredTab := fresh.session.Tab(tab.ID)
	if restoredTab == nil {
		t.Fatalf("restored daemon has no tab %s", tab.ID)
	}
	if restoredTab.ProjectID != dst.ID {
		t.Errorf("restored tab.ProjectID = %q, want %q — the client would skip it "+
			"and it would vanish from the sidebar", restoredTab.ProjectID, dst.ID)
	}
}
