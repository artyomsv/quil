package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// overlayReports collects every OverlayVisible value that reached the wire,
// keyed by pane id (last report wins, like the daemon).
//
// Every assertion in this file is about what was REPORTED, never about
// tab.overlayVisible: the daemon's idle timer has no other source of truth, so
// a flag that is right locally and never sent is exactly the bug.
func overlayReports(t *testing.T, conn *fakeConn) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, sent := range conn.sent {
		if sent.Type != ipc.MsgUpdatePane {
			continue
		}
		var p ipc.UpdatePanePayload
		if err := sent.DecodePayload(&p); err != nil || p.OverlayVisible == nil {
			continue
		}
		out[p.PaneID] = *p.OverlayVisible
	}
	return out
}

// A tab switch changes which overlay is on screen without touching any
// overlayVisible flag — only the active tab of a project renders. Alt+1..9 away
// from a tab with an open overlay must therefore report it hidden, or the sweep
// skips that overlay forever (its OverlayHiddenAt stays zero).
func TestSwitchTab_ReportsOverlayVisibilityForBothTabs(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	from := NewTabModel("tab-1", "one")
	from.overlayPane = &PaneModel{ID: "ov-left"}
	from.overlayVisible = true
	to := NewTabModel("tab-2", "two")
	// overlayVisible survives a tab switch by design, so a tab entered with
	// this set is showing its overlay again the moment it becomes active.
	to.overlayPane = &PaneModel{ID: "ov-entered"}
	to.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(from, to)}

	runCmd(m.switchTab(1))

	got := overlayReports(t, conn)
	if v, ok := got["ov-left"]; !ok || v {
		t.Errorf("tab being left reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
	if v, ok := got["ov-entered"]; !ok || !v {
		t.Errorf("tab being entered reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

// A tab with no overlay must stay off the wire entirely — a report naming a
// pane the daemon has no overlay for is noise on the critical queue.
func TestSwitchTab_TabsWithoutOverlaysReportNothing(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{
		cfg:      config.Default(),
		client:   conn,
		projects: oneProject(NewTabModel("tab-1", "one"), NewTabModel("tab-2", "two")),
	}

	runCmd(m.switchTab(1))

	for _, sent := range conn.sent {
		if sent.Type == ipc.MsgUpdatePane {
			t.Errorf("a tab switch between overlay-less tabs sent %s", sent.Type)
		}
	}
}

// After an attach round the daemon's copy of visibility can be stale in EITHER
// direction, and the dangerous one is stale-hidden: a transient last-client
// disconnect stamps every overlay hidden, and if the reconnecting client never
// re-reports, the sweep destroys a lazygit the user is looking at. Reporting the
// current truth for every overlay after attach is what closes both.
func TestAttachAllDests_ReportsCurrentOverlayVisibility(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	onScreen := NewTabModel("tab-1", "one")
	onScreen.overlayPane = &PaneModel{ID: "ov-onscreen"}
	onScreen.overlayVisible = true
	background := NewTabModel("tab-2", "two")
	background.overlayPane = &PaneModel{ID: "ov-background"}
	background.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(onScreen, background)}

	runCmd(m.attachAllDests())

	got := overlayReports(t, conn)
	if v, ok := got["ov-onscreen"]; !ok || !v {
		t.Errorf("the overlay on screen reported visible=%v (reported=%v); want an explicit true — "+
			"a daemon that stamped it hidden on a transient disconnect will destroy it", v, ok)
	}
	if v, ok := got["ov-background"]; !ok || v {
		t.Errorf("a background tab's overlay reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
}

// attachAllDests reruns on every WindowSizeMsg; only a round that attached
// something may re-assert this client's view.
func TestAttachAllDests_AlreadyAttachedReportsNothing(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	tab := NewTabModel("tab-1", "one")
	tab.overlayPane = &PaneModel{ID: "ov"}
	tab.overlayVisible = true
	m := &Model{
		cfg:      config.Default(),
		client:   conn,
		projects: oneProject(tab),
		attached: map[string]bool{"": true},
	}

	runCmd(m.attachAllDests())

	if len(overlayReports(t, conn)) != 0 {
		t.Errorf("a no-op attach round re-reported visibility: %v", overlayReports(t, conn))
	}
}
