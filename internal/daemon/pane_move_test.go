package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// movePaneFixture builds one project holding two tabs: A with panes a1, a2,
// a3 (in that order) and B with pane b1.
func movePaneFixture(t *testing.T) (sm *SessionManager, a, b, a1, a2, a3, b1 string) {
	t.Helper()
	sm = NewSessionManager(100)
	p := sm.CreateProject("alpha", "/work/alpha")
	a = sm.CreateTabInProject(p.ID, "A").ID
	b = sm.CreateTabInProject(p.ID, "B").ID
	pane := func(tabID string) string {
		t.Helper()
		pn, err := sm.CreatePane(tabID, "")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		return pn.ID
	}
	a1, a2, a3 = pane(a), pane(a), pane(a)
	b1 = pane(b)
	return sm, a, b, a1, a2, a3, b1
}

func TestMovePane_UpdatesBothSidesOfTheLink(t *testing.T) {
	sm, a, b, a1, _, _, _ := movePaneFixture(t)

	from, res := sm.MovePane(a1, b)

	if res != movePaneMoved {
		t.Fatalf("res = %v, want movePaneMoved", res)
	}
	if from != a {
		t.Errorf("from = %q, want the source tab %q", from, a)
	}
	if indexOfString(sm.tabs[a].Panes, a1) >= 0 {
		t.Errorf("src.Panes %v still lists the moved pane", sm.tabs[a].Panes)
	}
	dst := sm.tabs[b].Panes
	if len(dst) == 0 || dst[len(dst)-1] != a1 {
		t.Errorf("dst.Panes = %v, want it ending with the moved pane", dst)
	}
	if got := sm.panes[a1].CurrentTabID(); got != b {
		t.Errorf("pane.CurrentTabID() = %q, want %q", got, b)
	}
}

func TestMovePane_PreservesSourceOrder(t *testing.T) {
	sm, a, b, a1, a2, a3, _ := movePaneFixture(t)

	sm.MovePane(a2, b)

	if want := []string{a1, a3}; !reflect.DeepEqual(sm.tabs[a].Panes, want) {
		t.Errorf("src.Panes = %v, want %v", sm.tabs[a].Panes, want)
	}
}

// The corrupt-snapshot shape: a pane already listed by the target, which
// restore can reach because it copies each tab's panes verbatim.
func TestMovePane_NeverListsThePaneTwice(t *testing.T) {
	sm, _, b, a1, _, _, _ := movePaneFixture(t)
	sm.tabs[b].Panes = append(sm.tabs[b].Panes, a1)

	sm.MovePane(a1, b)

	seen := map[string]bool{}
	for _, id := range sm.tabs[b].Panes {
		if seen[id] {
			t.Fatalf("pane %s is listed twice in dst.Panes %v", id, sm.tabs[b].Panes)
		}
		seen[id] = true
	}
}

// assertMoveRefused checks a refusal left every tab's pane list and the
// pane's TabID exactly as they were.
func assertMoveRefused(t *testing.T, sm *SessionManager, paneID, tabID string, want movePaneResult) {
	t.Helper()
	before := snapshotAll(sm)
	var beforeTab string
	if p := sm.panes[paneID]; p != nil {
		beforeTab = p.CurrentTabID()
	}

	from, res := sm.MovePane(paneID, tabID)

	if res != want {
		t.Fatalf("res = %v, want %v", res, want)
	}
	if from != "" {
		t.Errorf("from = %q, want empty on a refusal", from)
	}
	if after := snapshotAll(sm); !reflect.DeepEqual(before, after) {
		t.Errorf("state changed on a refused move:\nbefore=%+v\nafter=%+v", before, after)
	}
	if p := sm.panes[paneID]; p != nil && p.CurrentTabID() != beforeTab {
		t.Errorf("pane.TabID = %q, want the unchanged %q", p.CurrentTabID(), beforeTab)
	}
}

func TestMovePane_SameTabIsNoop(t *testing.T) {
	sm, a, _, a1, _, _, _ := movePaneFixture(t)
	assertMoveRefused(t, sm, a1, a, movePaneNoop)
}

func TestMovePane_UnknownPane(t *testing.T) {
	sm, _, b, _, _, _, _ := movePaneFixture(t)
	assertMoveRefused(t, sm, "pane-gone", b, movePaneUnknownPane)
}

func TestMovePane_UnknownTab(t *testing.T) {
	sm, _, _, a1, _, _, _ := movePaneFixture(t)
	assertMoveRefused(t, sm, a1, "tab-gone", movePaneUnknownTab)
}

// A template tab no client has laid out yet is built from Tab.Panes, so a pane
// leaving or joining it first changes the tree the template produces.
func TestMovePane_TemplatePendingRefused(t *testing.T) {
	t.Run("pending source", func(t *testing.T) {
		sm, a, b, a1, _, _, _ := movePaneFixture(t)
		sm.tabs[a].TemplateLayout = "columns"
		assertMoveRefused(t, sm, a1, b, movePaneTemplatePending)
	})
	t.Run("pending target", func(t *testing.T) {
		sm, _, b, a1, _, _, _ := movePaneFixture(t)
		sm.tabs[b].TemplateLayout = "columns"
		assertMoveRefused(t, sm, a1, b, movePaneTemplatePending)
	})
	t.Run("laid-out template moves", func(t *testing.T) {
		sm, a, b, a1, _, _, _ := movePaneFixture(t)
		sm.tabs[a].TemplateLayout = "columns"
		sm.tabs[a].Layout = json.RawMessage(`{"pane_id":"x"}`)
		if _, res := sm.MovePane(a1, b); res != movePaneMoved {
			t.Errorf("res = %v, want movePaneMoved once the template tab has a layout", res)
		}
	})
}

func TestMovePane_LeavesProjectsAndActiveTabAlone(t *testing.T) {
	sm, _, b, a1, _, _, _ := movePaneFixture(t)
	activeTab, activeProject := sm.activeTab, sm.activeProject
	projects := sm.Projects()
	tabOrder := append([]string(nil), sm.tabOrder...)

	sm.MovePane(a1, b)

	if sm.activeTab != activeTab || sm.activeProject != activeProject {
		t.Errorf("active tab/project = %q/%q, want the unchanged %q/%q",
			sm.activeTab, sm.activeProject, activeTab, activeProject)
	}
	if got := sm.Projects(); !reflect.DeepEqual(got, projects) {
		t.Errorf("projects changed:\nbefore=%+v\nafter=%+v", projects, got)
	}
	if !reflect.DeepEqual(sm.tabOrder, tabOrder) {
		t.Errorf("tabOrder = %v, want the unchanged %v", sm.tabOrder, tabOrder)
	}
}

// --- Handler tests -----------------------------------------------------

func movePaneMsg(t *testing.T, paneID, tabID string) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgMovePane, ipc.MovePanePayload{PaneID: paneID, TabID: tabID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

func moveTestPane(t *testing.T, d *Daemon, tabID string) *Pane {
	t.Helper()
	p, err := d.session.CreatePane(tabID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	setPaneType(p, "terminal")
	return p
}

// movePaneHandlerFixture is one project holding src (two panes) and dst (one
// pane), plus a third tab so dissolving src never empties the project.
func movePaneHandlerFixture(t *testing.T, d *Daemon) (src, dst *Tab, s1, s2, d1 *Pane) {
	t.Helper()
	proj := d.session.CreateProject("alpha", t.TempDir())
	src = d.session.CreateTabInProject(proj.ID, "src")
	dst = d.session.CreateTabInProject(proj.ID, "dst")
	d.session.CreateTabInProject(proj.ID, "other")
	s1, s2 = moveTestPane(t, d, src.ID), moveTestPane(t, d, src.ID)
	d1 = moveTestPane(t, d, dst.ID)
	return src, dst, s1, s2, d1
}

func TestHandleMovePane_SchedulesOneSnapshot(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	_, dst, _, s2, _ := movePaneHandlerFixture(t, d)

	d.handleMessage(nil, movePaneMsg(t, s2.ID, dst.ID))

	if s2.CurrentTabID() != dst.ID {
		t.Fatalf("pane is in %q, want %q", s2.CurrentTabID(), dst.ID)
	}
	if len(d.snapshotCh) != 1 {
		t.Errorf("snapshotCh len = %d, want 1 — a move must be persisted", len(d.snapshotCh))
	}
}

func TestHandleMovePane_NoopSchedulesNothing(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	src, _, s1, _, _ := movePaneHandlerFixture(t, d)

	d.handleMessage(nil, movePaneMsg(t, s1.ID, src.ID))

	if len(d.snapshotCh) != 0 {
		t.Errorf("snapshotCh len = %d, want 0 — a same-tab move changes nothing", len(d.snapshotCh))
	}
}

// Moving a tab's last pane out dissolves the tab. It must NOT be refilled with
// a shell the way a close is (ensureTabNotEmpty): that pane is one the user
// never asked for. Mutation guard: swapping dissolveEmptyTab for
// ensureTabNotEmpty adds a pane, so the total count check fails.
func TestHandleMovePane_LastPaneDissolvesSourceTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	proj := d.session.CreateProject("alpha", t.TempDir())
	src := d.session.CreateTabInProject(proj.ID, "src")
	dst := d.session.CreateTabInProject(proj.ID, "dst")
	only := moveTestPane(t, d, src.ID)
	moveTestPane(t, d, dst.ID)
	total := len(d.session.AllPanes())

	d.handleMessage(nil, movePaneMsg(t, only.ID, dst.ID))

	if d.session.Tab(src.ID) != nil {
		t.Error("the emptied source tab still exists")
	}
	if p, _ := projectByID(d, proj.ID); indexOfString(p.TabIDs, src.ID) >= 0 {
		t.Errorf("project TabIDs %v still lists the dissolved tab", p.TabIDs)
	}
	if only.CurrentTabID() != dst.ID || indexOfString(d.session.Tab(dst.ID).Panes, only.ID) < 0 {
		t.Errorf("the moved pane is not in the target tab")
	}
	if got := len(d.session.AllPanes()); got != total {
		t.Errorf("pane count = %d, want the unchanged %d — the move added a pane", got, total)
	}
}

func TestHandleMovePane_OverlayLeftBehindGoesWithTheTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	proj := d.session.CreateProject("alpha", t.TempDir())
	src := d.session.CreateTabInProject(proj.ID, "src")
	dst := d.session.CreateTabInProject(proj.ID, "dst")
	normal := moveTestPane(t, d, src.ID)
	overlay := moveTestPane(t, d, src.ID)
	overlay.PluginMu.Lock()
	overlay.Overlay = true
	overlay.PluginMu.Unlock()
	moveTestPane(t, d, dst.ID)

	d.handleMessage(nil, movePaneMsg(t, normal.ID, dst.ID))

	if d.session.Tab(src.ID) != nil {
		t.Error("a source tab left holding only an overlay was not dissolved")
	}
	if d.session.Pane(overlay.ID) != nil {
		t.Error("the overlay left behind survived its tab")
	}
	if d.session.Pane(normal.ID) == nil {
		t.Error("the moved pane was destroyed")
	}
}

func TestHandleMovePane_EmptiedProjectGetsShellTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	srcProj := d.session.CreateProject("alpha", t.TempDir())
	dstProj := d.session.CreateProject("beta", t.TempDir())
	src := d.session.CreateTabInProject(srcProj.ID, "only")
	dst := d.session.CreateTabInProject(dstProj.ID, "dst")
	moving := moveTestPane(t, d, src.ID)
	moveTestPane(t, d, dst.ID)

	d.handleMessage(nil, movePaneMsg(t, moving.ID, dst.ID))

	p, ok := projectByID(d, srcProj.ID)
	if !ok {
		t.Fatalf("source project %s is gone", srcProj.ID)
	}
	if len(p.TabIDs) != 1 {
		t.Fatalf("source project holds %d tabs, want exactly 1 (the recovery Shell)", len(p.TabIDs))
	}
	if tab := d.session.Tab(p.TabIDs[0]); tab == nil || tab.Name != "Shell" {
		t.Errorf("source project's tab = %+v, want one named Shell", tab)
	}
	if moving.CurrentTabID() != dst.ID {
		t.Errorf("the moved pane is in %q, want %q", moving.CurrentTabID(), dst.ID)
	}
}

func TestHandleMovePane_KeptSourceTabIsNotRefilled(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	src, dst, s1, s2, _ := movePaneHandlerFixture(t, d)

	d.handleMessage(nil, movePaneMsg(t, s2.ID, dst.ID))

	if got := d.session.Tab(src.ID).Panes; !reflect.DeepEqual(got, []string{s1.ID}) {
		t.Errorf("src.Panes = %v, want exactly [%s]", got, s1.ID)
	}
}

// dissolveEmptyTab reads the pane list and destroys in two lock holds. A pane
// another client moves or creates into the tab in between must survive: the
// destroy is conditional on the list read. Mutation guard: a plain
// DestroyTab in dissolveTabHolding (or in DestroyTabIfPanes) kills the
// arrival and fails both subtests.
func TestDissolveEmptyTab_KeepsAPaneThatArrivedAfterTheRead(t *testing.T) {
	arrive := map[string]func(t *testing.T, d *Daemon, tab, other *Tab) *Pane{
		"moved in": func(t *testing.T, d *Daemon, tab, other *Tab) *Pane {
			p := moveTestPane(t, d, other.ID)
			if _, res := d.session.MovePane(p.ID, tab.ID); res != movePaneMoved {
				t.Fatalf("setup: MovePane res = %v", res)
			}
			return p
		},
		"created": func(t *testing.T, d *Daemon, tab, _ *Tab) *Pane {
			return moveTestPane(t, d, tab.ID)
		},
	}
	for name, land := range arrive {
		t.Run(name, func(t *testing.T) {
			d := overlayTestDaemon(t, config.Default())
			proj := d.session.CreateProject("alpha", t.TempDir())
			emptied := d.session.CreateTabInProject(proj.ID, "emptied")
			other := d.session.CreateTabInProject(proj.ID, "other")
			moveTestPane(t, d, other.ID) // keeps other alive

			// The first half's read: the emptied tab holds nothing.
			var read []string
			for _, p := range d.session.Panes(emptied.ID) {
				read = append(read, p.ID)
			}
			arrived := land(t, d, emptied, other)

			if d.dissolveTabHolding(emptied.ID, read) {
				t.Error("the tab was dissolved although a pane arrived after the read")
			}
			if d.session.Tab(emptied.ID) == nil {
				t.Fatal("the tab is gone")
			}
			if d.session.Pane(arrived.ID) == nil {
				t.Error("the pane that arrived was destroyed with the tab")
			}
		})
	}
}

func TestDestroyTabIfPanes(t *testing.T) {
	t.Run("same set in another order destroys", func(t *testing.T) {
		sm, a, _, a1, a2, a3, _ := movePaneFixture(t)
		ok, err := sm.DestroyTabIfPanes(a, []string{a3, a1, a2})
		if err != nil || !ok {
			t.Fatalf("ok, err = %v, %v; want true, nil", ok, err)
		}
		if sm.tabs[a] != nil || sm.panes[a1] != nil {
			t.Error("the tab or its panes survived a matching destroy")
		}
	})
	t.Run("a pane that arrived keeps everything", func(t *testing.T) {
		sm, a, _, a1, a2, a3, b1 := movePaneFixture(t)
		want := []string{a1, a2, a3}
		sm.MovePane(b1, a)
		before := snapshotAll(sm)

		ok, err := sm.DestroyTabIfPanes(a, want)
		if err != nil || ok {
			t.Fatalf("ok, err = %v, %v; want false, nil", ok, err)
		}
		if after := snapshotAll(sm); !reflect.DeepEqual(before, after) {
			t.Error("state changed on a declined destroy")
		}
	})
	t.Run("unknown tab", func(t *testing.T) {
		sm, _, _, _, _, _, _ := movePaneFixture(t)
		if ok, err := sm.DestroyTabIfPanes("tab-gone", nil); ok || err == nil {
			t.Errorf("ok, err = %v, %v; want false and an error", ok, err)
		}
	})
}

// assertHandlerRefused drives a move the handler must refuse and checks that
// nothing moved and nothing was scheduled.
func assertHandlerRefused(t *testing.T, d *Daemon, paneID, tabID string) {
	t.Helper()
	before := snapshotAll(d.session)
	var beforeTab string
	if p := d.session.Pane(paneID); p != nil {
		beforeTab = p.CurrentTabID()
	}

	d.handleMessage(nil, movePaneMsg(t, paneID, tabID))

	if after := snapshotAll(d.session); !reflect.DeepEqual(before, after) {
		t.Errorf("state changed on a refused move:\nbefore=%+v\nafter=%+v", before, after)
	}
	if p := d.session.Pane(paneID); p != nil && p.CurrentTabID() != beforeTab {
		t.Errorf("pane moved to %q on a refused move", p.CurrentTabID())
	}
	if len(d.snapshotCh) != 0 {
		t.Errorf("snapshotCh len = %d, want 0 on a refusal", len(d.snapshotCh))
	}
}

func TestHandleMovePane_RefusesOverlay(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	_, dst, _, s2, _ := movePaneHandlerFixture(t, d)
	s2.PluginMu.Lock()
	s2.Overlay = true
	s2.PluginMu.Unlock()
	assertHandlerRefused(t, d, s2.ID, dst.ID)
}

func TestHandleMovePane_RefusesPreparingWorktree(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	_, dst, _, s2, _ := movePaneHandlerFixture(t, d)
	s2.PluginMu.Lock()
	s2.PreparingWorktree = "feat/x"
	s2.PluginMu.Unlock()
	assertHandlerRefused(t, d, s2.ID, dst.ID)
}

func TestHandleMovePane_RefusesWhileWorktreeAddTargetsSource(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	src, dst, _, s2, _ := movePaneHandlerFixture(t, d)
	tab := src.ID
	d.worktreeAddTab.Store(&tab)
	assertHandlerRefused(t, d, s2.ID, dst.ID)
}

func TestHandleMovePane_RefusesWhileWorktreeAddTargetsTarget(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	_, dst, _, s2, _ := movePaneHandlerFixture(t, d)
	tab := dst.ID
	d.worktreeAddTab.Store(&tab)
	assertHandlerRefused(t, d, s2.ID, dst.ID)
}

func TestHandleMovePane_RefusesTabWithPreparingPlaceholder(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	_, dst, _, s2, d1 := movePaneHandlerFixture(t, d)
	d1.PluginMu.Lock()
	d1.PreparingWorktree = "feat/x"
	d1.PluginMu.Unlock()
	assertHandlerRefused(t, d, s2.ID, dst.ID)
}

// A lazily restored, Pending pane moved into a tab its project is showing must
// be spawned, or it sits on the restore indicator with no process behind it.
// The global active tab is the SOURCE, so the handler's unconditional
// ensureTabSpawned(ActiveTabID()) cannot be what spawns it.
func TestHandleMovePane_SpawnsPendingPaneInTheTargetSelection(t *testing.T) {
	d := newTestDaemon(t)
	src := d.session.CreateProject("alpha", t.TempDir())
	dst := d.session.CreateProject("beta", t.TempDir())
	srcTab := d.session.CreateTabInProject(src.ID, "src")
	dstTab := d.session.CreateTabInProject(dst.ID, "dst") // dst's ActiveTab
	moveTestPane(t, d, srcTab.ID)
	pending := moveTestPane(t, d, srcTab.ID)
	pending.Pending = true
	moveTestPane(t, d, dstTab.ID)

	d.session.SwitchProject(src.ID)
	if got := d.session.ActiveTabID(); got != srcTab.ID {
		t.Fatalf("setup: ActiveTabID() = %q, want the source tab", got)
	}
	if act, _ := d.session.ProjectActiveTab(dst.ID); act != dstTab.ID {
		t.Fatalf("setup: dst.ActiveTab = %q, want %q", act, dstTab.ID)
	}

	d.handleMessage(nil, movePaneMsg(t, pending.ID, dstTab.ID))

	if pending.CurrentTabID() != dstTab.ID {
		t.Fatalf("the pane did not move")
	}
	if pending.Pending || pending.PTY == nil {
		t.Error("a Pending pane moved into its project's active tab was left unspawned")
	}
}

// The pane records in a broadcast carry the tab that LISTS them. A move landing
// between SnapshotState and the build made the two disagree within one frame
// when the record read pane.TabID, and applyTemplateLayout bails on that.
func TestWorkspaceState_PaneTabIDFollowsTheListingTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	_, dst, _, s2, _ := movePaneHandlerFixture(t, d)

	check := func(state map[string]any) {
		t.Helper()
		listedIn := map[string]string{}
		for _, tab := range state["tabs"].([]map[string]any) {
			for _, pid := range tab["panes"].([]string) {
				listedIn[pid] = tab["id"].(string)
			}
		}
		for _, pane := range state["panes"].([]map[string]any) {
			id := pane["id"].(string)
			if pane["tab_id"] != listedIn[id] {
				t.Errorf("pane %s: tab_id = %v, but it is listed in %q", id, pane["tab_id"], listedIn[id])
			}
		}
	}

	t.Run("after a move", func(t *testing.T) {
		d.handleMessage(nil, movePaneMsg(t, s2.ID, dst.ID))
		check(d.buildWorkspaceState())
	})

	t.Run("a move between the snapshot and the build", func(t *testing.T) {
		activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
		src := s2.CurrentTabID()
		var back string
		for _, tab := range tabs {
			if tab.ID != src {
				back = tab.ID
				break
			}
		}
		if _, res := d.session.MovePane(s2.ID, back); res != movePaneMoved {
			t.Fatalf("setup: MovePane res = %v", res)
		}
		check(d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, true))
	})
}

// The in-flight record is set for exactly the length of the add, including on
// a failure path.
func TestWorktreeAddAndCreate_ClearsTheTabRecord(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	var during bool
	stubAdd(t, func(context.Context, string, string, string) error {
		during = d.worktreeAddingIn(tab.ID)
		return errors.New("fatal: boom")
	})

	resp := d.worktreeAddAndCreate(worktreeCreate(tab.ID, "/repo", "feat/x"))

	if resp.Error == "" {
		t.Fatal("setup: the stubbed add did not fail")
	}
	if !during {
		t.Error("worktreeAddingIn(tab) was false while the add ran")
	}
	if d.worktreeAddingIn(tab.ID) {
		t.Error("worktreeAddingIn(tab) is still true after the add returned")
	}
}
