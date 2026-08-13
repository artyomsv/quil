package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// runCmds executes every command a multi-return path handed back. Written out
// rather than looping runCmd so a caller cannot silently drop one — the whole
// point of these assertions is that the report reaches the wire.
func runCmds(cmds []tea.Cmd) { runCmd(tea.Batch(cmds...)) }

// overlayReports collects every OverlayVisible value that reached the wire,
// keyed by pane id (last report wins, like the daemon).
//
// Every assertion in this file is about what was REPORTED, never about
// tab.overlayVisible: the daemon's idle timer has no other source of truth, so
// a flag that is right locally and never sent is exactly the bug.
func overlayReports(t *testing.T, conn *fakeConn) map[string]bool {
	t.Helper()
	return overlayReportsIn(t, conn.sent)
}

// overlayReportsIn is the same over a raw send log, for the tests built on
// fakeSender rather than fakeConn.
func overlayReportsIn(t *testing.T, sent []*ipc.Message) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, sent := range sent {
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

// There are TWO attach owners, and the reconnect one (finishReconnect →
// attachToDest) is the path the dangerous half of the staleness runs on: the
// drop is itself what made the daemon stamp every overlay hidden, so a client
// that comes back with one still on screen and never re-reports watches the
// sweep destroy it. attachAllDests never runs for that flow (finishReconnect
// SETS m.attached[dest]), so reporting from it alone would miss exactly the
// case the report exists for.
func TestAttachToDest_ReportsCurrentOverlayVisibility(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	tab := NewTabModel("tab-1", "one")
	tab.overlayPane = &PaneModel{ID: "ov"}
	tab.overlayVisible = true
	// A tab on a daemon that never dropped: reporting it would re-stamp an
	// OverlayShownAt this reconnect knows nothing about.
	elsewhere := NewTabModel("tab-2", "two")
	elsewhere.Dest = "user@host"
	elsewhere.overlayPane = &PaneModel{ID: "ov-other-daemon"}
	elsewhere.overlayVisible = true
	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(tab, elsewhere)}

	runCmd(m.attachToDest(""))

	got := overlayReports(t, conn)
	if v, ok := got["ov"]; !ok || !v {
		t.Errorf("reconnect attach reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
	if _, ok := got["ov-other-daemon"]; ok {
		t.Error("a reconnect to one destination reported an overlay owned by another")
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

// jumpToPane is the same active-tab-changing choke point switchTab is — the
// destination for MCP set_active_pane, the notification sidebar's navigate,
// pane-history back-navigation, and the command palette's goToPane — and was
// missing this report entirely: a tab left through this path kept its
// overlay marked visible forever (never swept), and a tab entered through it
// could still be stamped hidden from an earlier hide, one sweep away from
// being destroyed while the user is looking at it.
func TestJumpToPane_ReportsOverlayVisibilityForBothTabs(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	from := NewTabModel("tab-1", "one")
	from.overlayPane = &PaneModel{ID: "ov-left"}
	from.overlayVisible = true
	to := tabWithPane("tab-2", "pane-in-to")
	// overlayVisible survives a tab switch by design, so a tab entered with
	// this set is showing its overlay again the moment it becomes active.
	to.overlayPane = &PaneModel{ID: "ov-entered"}
	to.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(from, to)}

	ok, cmd := m.jumpToPane("pane-in-to")
	if !ok {
		t.Fatal("jumpToPane reported failure for a pane that exists")
	}
	runCmd(cmd)

	got := overlayReports(t, conn)
	if v, ok := got["ov-left"]; !ok || v {
		t.Errorf("tab being left reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
	if v, ok := got["ov-entered"]; !ok || !v {
		t.Errorf("tab being entered reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

// jumpToNextBlocked (Alt+Shift+A, the palette's blocked-queue jump) is the
// third active-tab-changing choke point, and must report both halves exactly
// like switchTab and jumpToPane.
//
// Same project, two tabs — deliberately not a cross-project jump: the
// comments inside jumpToNextBlocked itself say the queue "routinely lands on
// another tab of the SAME project", and overlayOnScreen's own contract is
// scoped to a tab's own project ("switching projects moves no tab's
// activeTab" — see overlay.go), so a same-project jump is the case where the
// active-tab field this bug is about actually changes.
func TestJumpToNextBlocked_ReportsOverlayVisibilityForBothTabs(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()

	from := NewTabModel("tab-from", "from")
	from.overlayPane = &PaneModel{ID: "ov-left"}
	from.overlayVisible = true

	blocked := &PaneModel{ID: "pane-blocked"}
	blocked.blockedSince = time.Now()
	to := tabWith(blocked)
	// overlayVisible survives a tab switch by design, so a tab entered with
	// this set is showing its overlay again the moment it becomes active.
	to.overlayPane = &PaneModel{ID: "ov-entered"}
	to.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(from, to)}

	runCmd(m.jumpToNextBlocked())

	got := overlayReports(t, conn)
	if v, ok := got["ov-left"]; !ok || v {
		t.Errorf("tab being left reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
	if v, ok := got["ov-entered"]; !ok || !v {
		t.Errorf("tab being entered reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

// Switching PROJECTS takes the outgoing project's overlay off screen just as
// surely as a tab switch does, and reports nothing on its own — so a background
// project's overlay stayed marked visible for the life of the daemon and idle
// eviction, the mechanism the feature is named after, never applied to it.
func TestSwitchProject_ReportsOverlayVisibilityForBothProjects(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	leaving := NewTabModel("tab-a", "a")
	leaving.overlayPane = &PaneModel{ID: "ov-leaving"}
	leaving.overlayVisible = true
	entering := NewTabModel("tab-b", "b")
	entering.overlayPane = &PaneModel{ID: "ov-entering"}
	entering.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: []*ProjectModel{
		{ID: "proj-a", tabs: []*TabModel{leaving}},
		{ID: "proj-b", tabs: []*TabModel{entering}},
	}}

	runCmd(m.switchProject(1))

	got := overlayReports(t, conn)
	if v, ok := got["ov-leaving"]; !ok || v {
		t.Errorf("the outgoing project's overlay reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
	if v, ok := got["ov-entering"]; !ok || !v {
		t.Errorf("the incoming project's overlay reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

// twoProjectOverlayModel is twoProjectModel with an overlay on each project's
// tab and a recording connection — the shape every jumpToPane caller must
// produce two reports from.
func twoProjectOverlayModel(conn *fakeConn) Model {
	m := twoProjectModel()
	m.cfg = config.Default()
	m.client = conn
	m.projects[0].tabs[0].overlayPane = &PaneModel{ID: "ov-fg"}
	m.projects[0].tabs[0].overlayVisible = true
	m.projects[1].tabs[0].overlayPane = &PaneModel{ID: "ov-bg"}
	m.projects[1].tabs[0].overlayVisible = true
	return m
}

// jumpToPane returns a tea.Cmd its callers must PROPAGATE, and nothing pinned
// that. Every existing test at those call sites discards the command
// (`next, _ := ...`), so dropping it at any one of the four is invisible — and
// the consequence is silent: the tab left behind keeps its overlay marked
// visible, which exempts it from the idle sweep for the life of the daemon.
func TestJumpToPaneCallers_PropagateTheOverlayReport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(m *Model) tea.Cmd
	}{
		{"MCP set_active_pane", func(m *Model) tea.Cmd {
			_, cmd := m.Update(setActivePaneMsg{PaneID: "p-bg"})
			return cmd
		}},
		{"notification sidebar navigate", func(m *Model) tea.Cmd {
			m.notifications = NewNotificationCenter(30, 50)
			m.notifications.AddEvent(ipc.PaneEventPayload{ID: "e1", PaneID: "p-bg"})
			_, cmd := m.handleNotificationKey("enter")
			return cmd
		}},
		{"pane-history back", func(m *Model) tea.Cmd {
			m.paneHistory = []PaneRef{{ProjectID: "proj-bg", TabIndex: 0, PaneID: "p-bg"}}
			_, cmd := m.popPaneHistory()
			return cmd
		}},
		{"palette go to pane", func(m *Model) tea.Cmd {
			_, cmd := m.goToPane("p-bg")
			return cmd
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn := newFakeConn()
			// listenForMessages rides the same batch on one of these paths, and
			// runCmd walks a batch on this goroutine — a live recv channel would
			// park the test in Receive forever.
			close(conn.recv)
			m := twoProjectOverlayModel(conn)

			runCmd(tc.run(&m))

			got := overlayReports(t, conn)
			if v, ok := got["ov-fg"]; !ok || v {
				t.Errorf("the tab left reported visible=%v (reported=%v); want an explicit false — the caller dropped the cmd", v, ok)
			}
			if v, ok := got["ov-bg"]; !ok || !v {
				t.Errorf("the tab entered reported visible=%v (reported=%v); want an explicit true — the caller dropped the cmd", v, ok)
			}
		})
	}
}

// A CROSS-PROJECT jump used to report the tab it left as visible=TRUE: `from`
// is still its own project's active tab, so a tab-scoped overlayOnScreen
// answered "on screen" for a tab the user had just navigated away from —
// re-stamping OverlayShownAt to now and making it the LAST candidate for cap
// eviction, the exact LRU perturbation attachAllDests' scoping avoids.
func TestJumpToPane_CrossProjectReportsTheTabLeftAsHidden(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	leaving := NewTabModel("tab-a", "a")
	leaving.overlayPane = &PaneModel{ID: "ov-leaving"}
	leaving.overlayVisible = true
	entering := tabWithPane("tab-b", "pane-in-b")
	entering.overlayPane = &PaneModel{ID: "ov-entering"}
	entering.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: []*ProjectModel{
		{ID: "proj-a", tabs: []*TabModel{leaving}},
		{ID: "proj-b", tabs: []*TabModel{entering}},
	}}

	ok, cmd := m.jumpToPane("pane-in-b")
	if !ok {
		t.Fatal("jumpToPane reported failure for a pane that exists")
	}
	runCmd(cmd)

	got := overlayReports(t, conn)
	if v, ok := got["ov-leaving"]; !ok || v {
		t.Errorf("the tab left in another project reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
	if v, ok := got["ov-entering"]; !ok || !v {
		t.Errorf("the tab jumped to reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

// The other half of the same rule: a tab is only on screen when its project is
// the active one, so an overlay in a BACKGROUND project must report hidden
// wherever truth is reported — here, the post-attach sweep.
func TestAttachAllDests_BackgroundProjectsOverlayReportsHidden(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	foreground := NewTabModel("tab-a", "a")
	foreground.overlayPane = &PaneModel{ID: "ov-foreground"}
	foreground.overlayVisible = true
	background := NewTabModel("tab-b", "b")
	background.overlayPane = &PaneModel{ID: "ov-background"}
	background.overlayVisible = true

	m := &Model{cfg: config.Default(), client: conn, projects: []*ProjectModel{
		{ID: "proj-a", tabs: []*TabModel{foreground}},
		{ID: "proj-b", tabs: []*TabModel{background}},
	}}

	runCmd(m.attachAllDests())

	got := overlayReports(t, conn)
	if v, ok := got["ov-foreground"]; !ok || !v {
		t.Errorf("the active project's overlay reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
	if v, ok := got["ov-background"]; !ok || v {
		t.Errorf("a background PROJECT's overlay reported visible=%v (reported=%v); want an explicit false — "+
			"only the active project paints, so it is off screen and must idle-expire", v, ok)
	}
}

// The daemon can move a project's active tab behind the client's back — MCP
// switch_tab, a new tab, a tab destroy, or a second client — and the broadcast
// arm adopts it silently. Both drift directions are the ones this feature
// exists to prevent: the tab left behind keeps an overlay marked visible (never
// swept), and the tab entered can still be stamped hidden (one sweep from being
// destroyed while on screen).
func TestApplyWorkspaceState_DaemonSideActiveTabChangeReportsOverlayTruth(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn}
	state := func(active string) WorkspaceStateMsg {
		return WorkspaceStateMsg{
			ActiveTab: active,
			Tabs: []TabInfo{
				{ID: "tab-1", Name: "one", Panes: []string{"p-1", "ov-1"}},
				{ID: "tab-2", Name: "two", Panes: []string{"p-2", "ov-2"}},
			},
			Panes: []PaneInfo{
				{ID: "p-1", TabID: "tab-1", Type: "terminal"},
				{ID: "ov-1", TabID: "tab-1", Type: "lazygit", Overlay: true},
				{ID: "p-2", TabID: "tab-2", Type: "terminal"},
				{ID: "ov-2", TabID: "tab-2", Type: "lazygit", Overlay: true},
			},
		}
	}

	m.applyWorkspaceState(state("tab-1"), "")
	// Both tabs have an overlay open (Alt+G on each); only the active tab paints.
	for _, tab := range m.curTabs() {
		tab.overlayVisible = true
	}
	conn.sent = nil

	_, cmds := m.applyWorkspaceState(state("tab-2"), "")
	runCmds(cmds)

	got := overlayReports(t, conn)
	if v, ok := got["ov-1"]; !ok || v {
		t.Errorf("the tab the daemon left reported visible=%v (reported=%v); want an explicit false", v, ok)
	}
	if v, ok := got["ov-2"]; !ok || !v {
		t.Errorf("the tab the daemon entered reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}

// A broadcast that changes nothing about which tab is active must stay off the
// wire: broadcasts are frequent (the git ticker alone delivers one every 5 s),
// and every report is a must-deliver frame the daemon answers with another
// broadcast.
func TestApplyWorkspaceState_UnchangedActiveTabReportsNothing(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn}
	state := WorkspaceStateMsg{
		ActiveTab: "tab-1",
		Tabs:      []TabInfo{{ID: "tab-1", Name: "one", Panes: []string{"p-1", "ov-1"}}},
		Panes: []PaneInfo{
			{ID: "p-1", TabID: "tab-1", Type: "terminal"},
			{ID: "ov-1", TabID: "tab-1", Type: "lazygit", Overlay: true},
		},
	}

	m.applyWorkspaceState(state, "")
	m.curTabs()[0].overlayVisible = true
	conn.sent = nil

	_, cmds := m.applyWorkspaceState(state, "")
	runCmds(cmds)

	if got := overlayReports(t, conn); len(got) != 0 {
		t.Errorf("a broadcast that moved no active tab reported visibility: %v", got)
	}
}

// attachAllDests's post-attach overlay report must be scoped to the
// destination(s) that actually attached THIS round, never to every
// destination this client knows about. handleUpdatePane re-stamps
// OverlayShownAt on every visible=true report, even a repeat one, so
// re-reporting a destination that was already attached perturbs
// enforceOverlayCap's LRU eviction order for an overlay that had nothing to
// do with this round — the ordinary trigger being a local dest attaching
// instantly while a remote one is still mid-handshake and finishes later.
func TestAttachAllDests_ScopesOverlayReportToDestinationsThatJustAttached(t *testing.T) {
	t.Parallel()
	local := newFakeConn()
	gpu := newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	localTab := NewTabModel("tab-local", "local")
	localTab.overlayPane = &PaneModel{ID: "ov-local"}
	localTab.overlayVisible = true

	gpuTab := NewTabModel("tab-gpu", "gpu")
	gpuTab.Dest = "gpu01"
	gpuTab.overlayPane = &PaneModel{ID: "ov-gpu"}
	gpuTab.overlayVisible = true

	m := &Model{
		cfg:    config.Default(),
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{localTab}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{gpuTab}},
		},
		// The local destination already attached in an earlier round; only
		// gpu01 is new this round.
		attached: map[string]bool{"": true},
		// gpu01's project is the one ON SCREEN. overlayOnScreen answers for the
		// ACTIVE project, so a background project's overlay is off screen
		// whatever its flag says — and this test is about DESTINATION scoping,
		// so the visibility value must not be what makes it pass or fail.
		activeProject: 1,
	}

	runCmd(m.attachAllDests())

	if got := overlayReports(t, local); len(got) != 0 {
		t.Errorf("an already-attached destination's overlay was re-reported: %v", got)
	}
	got := overlayReports(t, gpu)
	if v, ok := got["ov-gpu"]; !ok || !v {
		t.Errorf("the newly-attached destination's overlay reported visible=%v (reported=%v); want an explicit true", v, ok)
	}
}
