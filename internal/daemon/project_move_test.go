package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// moveTabFixture builds two projects with their tabs created INTERLEAVED, so
// the global tab order disagrees with either project's own list — the same
// property mergeFixture uses, and for the same reason: an ordering assertion
// that happens to hold when the two orders coincide proves nothing.
//
// src ends up with three tabs (t1, t2, t3); dst with two (u1, u2). Global
// creation order: t1, u1, t2, u2, t3.
func moveTabFixture(t *testing.T) (sm *SessionManager, src, dst, t1, t2, t3, u1, u2 string) {
	t.Helper()
	sm = NewSessionManager(100)
	tab := func(projectID, name string) string {
		t.Helper()
		return sm.CreateTabInProject(projectID, name).ID
	}
	srcP := sm.CreateProject("alpha", "/work/alpha")
	dstP := sm.CreateProject("beta", "/work/beta")
	src, dst = srcP.ID, dstP.ID

	t1 = tab(src, "one")
	u1 = tab(dst, "uno")
	t2 = tab(src, "two")
	u2 = tab(dst, "dos")
	t3 = tab(src, "three")
	return sm, src, dst, t1, t2, t3, u1, u2
}

// sessionSnapshot is a value copy of everything SnapshotState reports, so two
// calls can be compared with reflect.DeepEqual to prove an operation left the
// daemon byte-identical.
type sessionSnapshot struct {
	activeTab     string
	tabs          []*Tab
	panesByTab    map[string][]*Pane
	projects      []Project
	activeProject string
}

func snapshotAll(sm *SessionManager) sessionSnapshot {
	at, tabs, panes, projects, ap := sm.SnapshotState()
	return sessionSnapshot{at, tabs, panes, projects, ap}
}

func TestMoveTab_UpdatesBothSidesOfTheLink(t *testing.T) {
	sm, src, dst, t1, _, _, _, _ := moveTabFixture(t)

	from, res := sm.MoveTab(t1, dst)

	if res != moveTabMoved {
		t.Fatalf("res = %v, want moveTabMoved", res)
	}
	if from != src {
		t.Errorf("from = %q, want the source project %q", from, src)
	}
	if got := sm.tabs[t1].ProjectID; got != dst {
		t.Errorf("tab.ProjectID = %q, want %q", got, dst)
	}
	if indexOfString(sm.projects[src].TabIDs, t1) >= 0 {
		t.Errorf("src.TabIDs %v still lists the moved tab", sm.projects[src].TabIDs)
	}
	dstTabs := sm.projects[dst].TabIDs
	if len(dstTabs) == 0 || dstTabs[len(dstTabs)-1] != t1 {
		t.Errorf("dst.TabIDs = %v, want it ending with the moved tab", dstTabs)
	}
}

func TestMoveTab_AppendsAtTheEndAndBecomesTargetActive(t *testing.T) {
	sm, _, dst, t1, _, _, _, _ := moveTabFixture(t)

	sm.MoveTab(t1, dst)

	dstTabs := sm.projects[dst].TabIDs
	if dstTabs[len(dstTabs)-1] != t1 {
		t.Fatalf("dst.TabIDs = %v, want the moved tab last", dstTabs)
	}
	if got := sm.projects[dst].ActiveTab; got != t1 {
		t.Errorf("dst.ActiveTab = %q, want the moved tab %q", got, t1)
	}
}

// The successor is chosen exactly as DestroyTab chooses one: the neighbour
// that slides into the moved tab's slot, from the OWNING project's own list.
func TestMoveTab_SourceActivePicksNeighbourLikeDestroyTab(t *testing.T) {
	t.Run("middle of three", func(t *testing.T) {
		sm, src, dst, _, t2, t3, _, _ := moveTabFixture(t)
		sm.projects[src].ActiveTab = t2

		sm.MoveTab(t2, dst)

		if got := sm.projects[src].ActiveTab; got != t3 {
			t.Errorf("src.ActiveTab = %q, want %q, the tab that slid into the moved "+
				"one's slot", got, t3)
		}
	})

	t.Run("last tab", func(t *testing.T) {
		sm, src, dst, _, t2, t3, _, _ := moveTabFixture(t)
		sm.projects[src].ActiveTab = t3

		sm.MoveTab(t3, dst)

		if got := sm.projects[src].ActiveTab; got != t2 {
			t.Errorf("src.ActiveTab = %q, want the previous tab %q", got, t2)
		}
	})
}

func TestMoveTab_NonActiveTabLeavesSourceActiveAlone(t *testing.T) {
	sm, src, dst, t1, t2, _, _, _ := moveTabFixture(t)
	sm.projects[src].ActiveTab = t2

	sm.MoveTab(t1, dst)

	if got := sm.projects[src].ActiveTab; got != t2 {
		t.Errorf("src.ActiveTab = %q, want it untouched at %q", got, t2)
	}
}

func TestMoveTab_GlobalActiveFollowsSourceSuccessor(t *testing.T) {
	sm, src, dst, _, t2, t3, _, _ := moveTabFixture(t)
	sm.activeProject = src
	sm.activeTab = t2

	sm.MoveTab(t2, dst)

	if got := sm.activeTab; got != t3 {
		t.Errorf("activeTab = %q, want the successor %q", got, t3)
	}
}

func TestMoveTab_GlobalActiveFollowsWhenTargetIsActiveProject(t *testing.T) {
	sm, _, dst, t1, _, _, u1, _ := moveTabFixture(t)
	sm.activeProject = dst
	sm.activeTab = u1

	sm.MoveTab(t1, dst)

	if got := sm.activeTab; got != t1 {
		t.Errorf("activeTab = %q, want the moved tab %q — dst is the active project",
			got, t1)
	}
}

func TestMoveTab_ReanchorsTheGlobalTabOrder(t *testing.T) {
	sm, _, dst, t1, t2, t3, u1, u2 := moveTabFixture(t)

	sm.MoveTab(t2, dst)

	// t2 sits right after dst's previous last tab (u2), and t1/u1/u2/t3 keep
	// their relative order — no other project's tab moved.
	want := []string{t1, u1, u2, t2, t3}
	if !reflect.DeepEqual(sm.tabOrder, want) {
		t.Fatalf("tabOrder = %v, want %v", sm.tabOrder, want)
	}
}

func TestMoveTab_SameProjectIsNoop(t *testing.T) {
	sm, src, _, t1, _, _, _, _ := moveTabFixture(t)
	before := snapshotAll(sm)

	from, res := sm.MoveTab(t1, src)

	if res != moveTabNoop {
		t.Fatalf("res = %v, want moveTabNoop", res)
	}
	if from != "" {
		t.Errorf("from = %q, want empty on a noop", from)
	}
	if after := snapshotAll(sm); !reflect.DeepEqual(before, after) {
		t.Errorf("state changed on a same-project move:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestMoveTab_UnknownTab(t *testing.T) {
	sm, _, dst, _, _, _, _, _ := moveTabFixture(t)
	before := snapshotAll(sm)

	from, res := sm.MoveTab("tab-gone", dst)

	if res != moveTabUnknownTab {
		t.Fatalf("res = %v, want moveTabUnknownTab", res)
	}
	if from != "" {
		t.Errorf("from = %q, want empty", from)
	}
	if after := snapshotAll(sm); !reflect.DeepEqual(before, after) {
		t.Errorf("state changed on an unknown tab:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestMoveTab_UnknownProject(t *testing.T) {
	sm, _, _, t1, _, _, _, _ := moveTabFixture(t)
	before := snapshotAll(sm)

	from, res := sm.MoveTab(t1, "proj-gone")

	if res != moveTabUnknownProject {
		t.Fatalf("res = %v, want moveTabUnknownProject", res)
	}
	if from != "" {
		t.Errorf("from = %q, want empty", from)
	}
	if after := snapshotAll(sm); !reflect.DeepEqual(before, after) {
		t.Errorf("state changed on an unknown project:\nbefore=%+v\nafter=%+v", before, after)
	}
}

// The corrupt-snapshot shape: a tab already listed under two projects, which
// restoreProjects can reach because it copies tab_ids verbatim with no
// uniqueness check across projects.
func TestMoveTab_NeverListsTheTabTwice(t *testing.T) {
	sm, _, dst, t1, _, _, _, _ := moveTabFixture(t)
	sm.projects[dst].TabIDs = append(sm.projects[dst].TabIDs, t1)

	sm.MoveTab(t1, dst)

	seen := map[string]bool{}
	for _, id := range sm.projects[dst].TabIDs {
		if seen[id] {
			t.Fatalf("tab %s is listed twice in dst.TabIDs %v", id, sm.projects[dst].TabIDs)
		}
		seen[id] = true
	}
}

// projectByID is a test helper: Projects() returns a value-copy slice, not a
// map, so a lookup by id is spelled as a loop everywhere in this package.
func projectByID(d *Daemon, id string) (Project, bool) {
	for _, p := range d.session.Projects() {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// --- Handler tests -----------------------------------------------------

// Drives the move over a REAL conn and counts actual workspace_state frames,
// rather than trusting the name — a plain d.handleMessage(nil, msg) call
// proves nothing about how many times broadcastState() itself fires, since
// d.server is nil in that shape and broadcastState is a no-op. The N
// back-to-back full workspace_state frames onto a 64-slot must-deliver queue
// is the documented 2026-08-09 force-disconnect shape (daemon-lifecycle.md),
// which is exactly what a move that broadcast per side-effect would risk.
func TestHandleMoveTab_BroadcastsOnceAndSchedulesSnapshot(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	src := d.session.CreateProject("alpha", "/work/alpha")
	dst := d.session.CreateProject("beta", "/work/beta")
	// A second tab in src so the move does not also trigger the empty-project
	// recovery path — this test is about the ordinary broadcast/snapshot pair.
	d.session.CreateTabInProject(src.ID, "other")
	tab := d.session.CreateTabInProject(src.ID, "moving")

	client := attachTestClient(t, sock)
	defer client.Close()
	frames := countWorkspaceFrames(client)
	waitUntil(t, "the attach broadcast to land", func() bool { return frames.Count() > 0 })
	frames.Reset()

	msg, err := ipc.NewMessage(ipc.MsgMoveTab, ipc.MoveTabPayload{TabID: tab.ID, ProjectID: dst.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if err := client.Send(msg); err != nil {
		t.Fatalf("send move_tab: %v", err)
	}

	waitUntil(t, "the move to broadcast", func() bool { return frames.Count() > 0 })
	time.Sleep(100 * time.Millisecond) // let a stray second frame land, if one would
	if n := frames.Count(); n != 1 {
		t.Errorf("move broadcast %d workspace_state frames, want exactly 1", n)
	}

	if len(d.snapshotCh) != 1 {
		t.Errorf("snapshotCh len = %d, want 1 — the move must schedule a snapshot "+
			"or it lives only in memory until the periodic ticker fires", len(d.snapshotCh))
	}
}

func TestHandleMoveTab_NoopSchedulesNothing(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	src := d.session.CreateProject("alpha", "/work/alpha")
	tab := d.session.CreateTabInProject(src.ID, "moving")

	msg, err := ipc.NewMessage(ipc.MsgMoveTab, ipc.MoveTabPayload{TabID: tab.ID, ProjectID: src.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleMessage(nil, msg)

	if len(d.snapshotCh) != 0 {
		t.Errorf("snapshotCh len = %d, want 0 — a same-project move is a no-op and "+
			"must not broadcast or snapshot", len(d.snapshotCh))
	}
}

func TestHandleMoveTab_EmptiedSourceGetsAShellTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	src := d.session.CreateProject("alpha", "/work/alpha")
	dst := d.session.CreateProject("beta", "/work/beta")
	tab := d.session.CreateTabInProject(src.ID, "only")

	msg, err := ipc.NewMessage(ipc.MsgMoveTab, ipc.MoveTabPayload{TabID: tab.ID, ProjectID: dst.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleMessage(nil, msg)

	srcAfter, ok := projectByID(d, src.ID)
	if !ok {
		t.Fatalf("source project %s is gone", src.ID)
	}
	if len(srcAfter.TabIDs) != 1 {
		t.Fatalf("source holds %d tabs, want exactly 1 (the recovery Shell)", len(srcAfter.TabIDs))
	}
	recovered := d.session.Tab(srcAfter.TabIDs[0])
	if recovered == nil || recovered.Name != "Shell" {
		t.Errorf("source's remaining tab = %+v, want a tab named Shell", recovered)
	}

	dstAfter, ok := projectByID(d, dst.ID)
	if !ok {
		t.Fatalf("destination project %s is gone", dst.ID)
	}
	if indexOfString(dstAfter.TabIDs, tab.ID) < 0 {
		t.Errorf("dst.TabIDs %v does not list the moved tab %s", dstAfter.TabIDs, tab.ID)
	}
}

// TestHandleMoveTab_SpawnsTheSourceSuccessorWhenTheActiveTabMovesOut mirrors
// TestHandleSwitchProjectSpawnsTheIncomingProjectsTab for the move-tab path.
//
// After a lazy restore, only sm.activeTab's panes are running and everything
// else is Pending. Moving the GLOBAL active tab out of its own project
// promotes the source's successor to sm.activeTab (SessionManager.MoveTab,
// mirroring DestroyTab) — and until handleMoveTab called ensureTabSpawned on
// whatever tab ended up active (rather than only on the moved tab), that
// promoted successor's panes stayed Pending forever: a restore indicator with
// no process behind it, the same failure the MsgSwitchProject arm exists to
// prevent.
func TestHandleMoveTab_SpawnsTheSourceSuccessorWhenTheActiveTabMovesOut(t *testing.T) {
	d := newTestDaemon(t)
	src := d.session.CreateProject("alpha", t.TempDir())
	dst := d.session.CreateProject("beta", t.TempDir())

	successorTab := d.session.CreateTabInProject(src.ID, "stays")
	movingTab := d.session.CreateTabInProject(src.ID, "moving")

	successorPane, err := d.session.CreatePane(successorTab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create successor pane: %v", err)
	}
	// The shape a lazy restore leaves behind: everything outside the active
	// tab is deferred.
	successorPane.Type = "terminal"
	successorPane.Pending = true

	d.session.SwitchProject(src.ID)
	d.session.SwitchTab(movingTab.ID)
	if got := d.session.ActiveTabID(); got != movingTab.ID {
		t.Fatalf("setup invariant broken: ActiveTabID() = %q, want the moving tab", got)
	}
	if successorPane.PTY != nil {
		t.Fatal("setup invariant broken: the successor's pane must still be Pending")
	}

	msg, err := ipc.NewMessage(ipc.MsgMoveTab, ipc.MoveTabPayload{
		TabID:     movingTab.ID,
		ProjectID: dst.ID,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleMessage(nil, msg)

	if got := d.session.ActiveTabID(); got != successorTab.ID {
		t.Fatalf("ActiveTabID() = %q after the move, want the promoted successor %q",
			got, successorTab.ID)
	}
	if successorPane.PTY == nil || successorPane.Pending {
		t.Error("moving the active tab out left its promoted successor unspawned — the " +
			"user lands on a restore indicator with no process behind it")
	}
}
