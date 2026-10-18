package daemon

import (
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Issue #262: closing a tab's last pane closes the tab. It used to refill the
// tab with a fresh shell, so Ctrl+W on a lone pane looked broken.

func destroyPaneMsg(t *testing.T, paneID string) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: paneID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

func destroyPaneReqMsg(t *testing.T, paneID string) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgDestroyPaneReq, ipc.DestroyPaneReqPayload{PaneID: paneID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.ID = "req-1"
	return msg
}

// Both close paths: the TUI's Ctrl+W (destroy_pane) and MCP destroy_pane
// (destroy_pane_req). Mutation guard: swapping closeTabIfEmpty back for
// ensureTabNotEmpty keeps the tab and adds a pane, so both checks fail.
func TestDestroyPane_LastPaneClosesTheTab(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  func(*testing.T, string) *ipc.Message
	}{
		{"destroy_pane", destroyPaneMsg},
		{"destroy_pane_req", destroyPaneReqMsg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := overlayTestDaemon(t, config.Default())
			proj := d.session.CreateProject("alpha", t.TempDir())
			closing := d.session.CreateTabInProject(proj.ID, "closing")
			kept := d.session.CreateTabInProject(proj.ID, "kept")
			only := moveTestPane(t, d, closing.ID)
			moveTestPane(t, d, kept.ID)
			total := len(d.session.AllPanes())

			d.handleMessage(nil, tc.msg(t, only.ID))

			if d.session.Tab(closing.ID) != nil {
				t.Error("the tab whose last pane closed still exists")
			}
			p, ok := projectByID(d, proj.ID)
			if !ok {
				t.Fatalf("project %s is gone", proj.ID)
			}
			if !reflect.DeepEqual(p.TabIDs, []string{kept.ID}) {
				t.Errorf("project TabIDs = %v, want exactly [%s]", p.TabIDs, kept.ID)
			}
			if got := len(d.session.AllPanes()); got != total-1 {
				t.Errorf("pane count = %d, want %d — a replacement pane was spawned", got, total-1)
			}
		})
	}
}

// The window is never left blank: a project that loses its only tab this way
// gets one fresh Shell tab, as after Alt+W.
func TestDestroyPane_ProjectsLastTabIsReplacedByAShellTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	proj := d.session.CreateProject("alpha", t.TempDir())
	only := d.session.CreateTabInProject(proj.ID, "only")
	pane := moveTestPane(t, d, only.ID)

	d.handleMessage(nil, destroyPaneMsg(t, pane.ID))

	if d.session.Tab(only.ID) != nil {
		t.Error("the closed tab still exists")
	}
	p, ok := projectByID(d, proj.ID)
	if !ok {
		t.Fatalf("project %s is gone", proj.ID)
	}
	if len(p.TabIDs) != 1 {
		t.Fatalf("project holds %d tabs, want exactly 1 (the recovery Shell)", len(p.TabIDs))
	}
	tab := d.session.Tab(p.TabIDs[0])
	if tab == nil || tab.Name != "Shell" || len(tab.Panes) != 1 {
		t.Errorf("project's tab = %+v, want one named Shell holding one pane", tab)
	}
}

func TestDestroyPane_TabWithOtherPanesIsKept(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	proj := d.session.CreateProject("alpha", t.TempDir())
	tab := d.session.CreateTabInProject(proj.ID, "two")
	gone := moveTestPane(t, d, tab.ID)
	stays := moveTestPane(t, d, tab.ID)

	d.handleMessage(nil, destroyPaneMsg(t, gone.ID))

	got := d.session.Tab(tab.ID)
	if got == nil {
		t.Fatal("a tab that still holds a pane was closed")
	}
	if !reflect.DeepEqual(got.Panes, []string{stays.ID}) {
		t.Errorf("tab.Panes = %v, want exactly [%s]", got.Panes, stays.ID)
	}
}

// An overlay does not keep a tab alive, and it goes with the tab.
func TestDestroyPane_OverlayLeftBehindGoesWithTheTab(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	proj := d.session.CreateProject("alpha", t.TempDir())
	closing := d.session.CreateTabInProject(proj.ID, "closing")
	kept := d.session.CreateTabInProject(proj.ID, "kept")
	normal := moveTestPane(t, d, closing.ID)
	overlay := moveTestPane(t, d, closing.ID)
	overlay.PluginMu.Lock()
	overlay.Overlay = true
	overlay.PluginMu.Unlock()
	moveTestPane(t, d, kept.ID)

	d.handleMessage(nil, destroyPaneMsg(t, normal.ID))

	if d.session.Tab(closing.ID) != nil {
		t.Error("a tab left holding only an overlay was not closed")
	}
	if d.session.Pane(overlay.ID) != nil {
		t.Error("the overlay left behind survived its tab")
	}
}
