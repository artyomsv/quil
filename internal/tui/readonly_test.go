package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

const roDest = "tcp:127.0.0.1:7878"

func readOnlyModel(t *testing.T, rights string) (Model, *fakeConn) {
	t.Helper()
	pane := NewPaneModel("pane-1", testRingBufSize)
	t.Cleanup(pane.Dispose)
	tab := NewTabModel("tab-1", "Shell")
	tab.Dest = roDest
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "pane-1"
	tab2 := NewTabModel("tab-2", "Two")
	tab2.Dest = roDest
	conn := newFakeConn()
	t.Cleanup(func() { close(conn.recv) })
	r := NewRouter(map[string]Client{roDest: conn})
	r.SetActiveDest(roDest)
	m := Model{
		cfg: config.Default(), client: r, tabDragFromIdx: -1, termFocused: true,
		// Like the repo's other Update-driven fixtures (attention_test.go,
		// broadcast_echo_test.go): Update reaches the notification centre.
		notifications: NewNotificationCenter(30, 50),
		projects:      []*ProjectModel{{ID: "proj-1", Name: "Default", Dest: roDest, tabs: []*TabModel{tab, tab2}}},
	}
	// Without the keymap, Ctrl+N never opens the dialog for EITHER rights
	// level, and the read-only "dialog stayed closed" assert passes vacuously.
	m.initKeymap()
	m.SetDestRights(roDest, rights)
	return m, conn
}

func actSent(conn *fakeConn) []string {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	var out []string
	for _, msg := range conn.sent {
		if !clientauth.IsView(msg.Type) {
			out = append(out, msg.Type)
		}
	}
	return out
}

func TestRouter_ReadOnlyDropsActMessages(t *testing.T) {
	conn := newFakeConn()
	defer close(conn.recv)
	r := NewRouter(map[string]Client{roDest: conn})
	r.SetDestRights(roDest, ipc.RightsReadOnly)
	act, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "p", Data: []byte("x")})
	stampDest(act, roDest)
	view, _ := ipc.NewMessage(ipc.MsgListPanesReq, struct{}{})
	stampDest(view, roDest)
	_ = r.Send(act)
	_ = r.Send(view)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.sent) != 1 || conn.sent[0].Type != ipc.MsgListPanesReq {
		t.Fatalf("sent %v, want only the view request", conn.sent)
	}
}

// Control: the same router with full rights delivers both.
func TestRouter_FullRightsSendsActMessages(t *testing.T) {
	conn := newFakeConn()
	defer close(conn.recv)
	r := NewRouter(map[string]Client{roDest: conn})
	r.SetDestRights(roDest, ipc.RightsFull)
	act, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "p", Data: []byte("x")})
	stampDest(act, roDest)
	_ = r.Send(act)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.sent) != 1 || conn.sent[0].Type != ipc.MsgPaneInput {
		t.Fatalf("control: sent %v, want the pane input", conn.sent)
	}
}

func driveUserActions(t *testing.T, m Model) Model {
	t.Helper()
	steps := []tea.Msg{
		tea.WindowSizeMsg{Width: 172, Height: 48},
		tea.KeyPressMsg{Code: 'a', Text: "a"},
		PaneOutputMsg{PaneID: "pane-1", Data: []byte("\x1b]7;file://host/tmp/elsewhere\x07$ ")},
		tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl},
	}
	for _, s := range steps {
		next, cmd := m.Update(s)
		runCmdNoWait(cmd)
		m = next.(Model)
	}
	return m
}

// A read-only TUI sends no act message.
func TestReadOnly_SendsNoActMessage(t *testing.T) {
	m, conn := readOnlyModel(t, ipc.RightsReadOnly)
	m = driveUserActions(t, m)
	if got := actSent(conn); len(got) != 0 {
		t.Fatalf("a read-only TUI sent act messages: %v", got)
	}
	if m.dialog != dialogNone {
		t.Fatal("Ctrl+N opened the create dialog on a read-only destination")
	}
	if !strings.Contains(m.renderStatusBar(), "[read-only]") {
		t.Fatal("status bar does not show [read-only]")
	}
}

// Control: the same steps with full rights DO send act messages and DO open
// the create dialog on Ctrl+N, so the test above can fail.
func TestReadWrite_SameStepsSendActs(t *testing.T) {
	m, conn := readOnlyModel(t, ipc.RightsFull)
	m = driveUserActions(t, m)
	if len(actSent(conn)) == 0 {
		t.Fatal("control: a full-rights TUI sent no act message for the same steps")
	}
	if m.dialog != dialogCreatePane {
		t.Fatalf("control: Ctrl+N on a full destination left dialog = %v, want dialogCreatePane", m.dialog)
	}
	if strings.Contains(m.renderStatusBar(), "[read-only]") {
		t.Fatal("control: a full destination shows [read-only]")
	}
}

// The status bar tag is absent for a destination with no recorded rights —
// the local socket and ssh — exactly as before.
func TestReadOnly_NoTagWithoutRights(t *testing.T) {
	m, _ := readOnlyModel(t, "")
	m.width, m.height = 172, 48
	if strings.Contains(m.renderStatusBar(), "[read-only]") {
		t.Fatal("a destination with no recorded rights shows [read-only]")
	}
}

// The create, close and rename actions on the palette and the pane/tab
// context menus stay listed but disabled on a read-only destination; the same
// rows are enabled on a full one.
func TestReadOnly_MenusGreyCreateCloseRename(t *testing.T) {
	for _, tc := range []struct {
		rights  string
		enabled bool
	}{{ipc.RightsReadOnly, false}, {ipc.RightsFull, true}} {
		t.Run(tc.rights, func(t *testing.T) {
			m, _ := readOnlyModel(t, tc.rights)
			m.sidebarOpen = true
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})

			// The palette, opened by its key.
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
			if m.dialog != dialogCommandPalette {
				t.Fatal("setup: the palette did not open")
			}
			palette := m.paletteDisplay()
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

			greyed := 0
			for _, c := range palette {
				if !readOnlyGreyedPalette[c.action] {
					continue
				}
				greyed++
				if !tc.enabled && c.enabled {
					t.Errorf("palette %q is enabled on a read-only destination", c.label)
				}
			}
			if greyed < 8 {
				t.Fatalf("only %d create/close/rename palette rows found — the set or the builder drifted", greyed)
			}
			// Rows the palette enables unconditionally on a full destination.
			for _, c := range palette {
				switch c.action {
				case palActNewPane, palActClosePane, palActRenamePane, palActRenameTab, palActCloseTab,
					palActCycleTabColor, palActMute, palActEager, palActRestartPane, palActProcesses:
					if c.enabled != tc.enabled {
						t.Errorf("palette %q enabled=%v, want %v", c.label, c.enabled, tc.enabled)
					}
				case palActNewProject:
					// It may target another destination: never greyed here.
					if !c.enabled {
						t.Errorf("palette %q is disabled", c.label)
					}
				}
			}

			// The pane menu, by its key (quick actions).
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
			assertCtxRows(t, m.ctxMenu.items, tc.enabled, ctxActRename, ctxActClose,
				ctxActMute, ctxActAttention, ctxActMarkDeletion, ctxActRestart)
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

			// The tab menu, by a right-click on the tab bar.
			spans := m.tabSpans()
			m = roUpdate(t, m, tea.MouseClickMsg{X: m.projectSidebarWidth() + spans[0].start + 1, Y: 0, Button: tea.MouseRight})
			assertCtxRows(t, m.ctxMenu.items, tc.enabled, ctxActRenameTab, ctxActTabColorList)
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

			// The project menu, by a right-click on its sidebar row.
			m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseRight})
			assertCtxRows(t, m.ctxMenu.items, tc.enabled, ctxActRenameProject, ctxActGroupList)
			// Client-side only: enabled whatever the rights.
			assertCtxRows(t, m.ctxMenu.items, true, ctxActDisconnectHost)
		})
	}
}

// ctxItemIndex is the index of the menu row with id.
func ctxItemIndex(t *testing.T, items []ctxMenuItem, id ctxMenuAction) int {
	t.Helper()
	for i, it := range items {
		if it.id == id {
			return i
		}
	}
	t.Fatalf("setup: menu row %d missing", id)
	return -1
}

// sidebarRowY is the screen row of the first sidebar row of kind.
func sidebarRowY(t *testing.T, m Model, kind string) int {
	t.Helper()
	for y, r := range m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight()) {
		if r.kind == kind {
			return y
		}
	}
	t.Fatalf("setup: no %q row in the sidebar", kind)
	return -1
}

func assertCtxRows(t *testing.T, items []ctxMenuItem, enabled bool, ids ...ctxMenuAction) {
	t.Helper()
	for _, id := range ids {
		found := false
		for _, it := range items {
			if it.id == id {
				found = true
				if it.enabled != enabled {
					t.Errorf("menu row %q enabled=%v, want %v", it.label, it.enabled, enabled)
				}
			}
		}
		if !found {
			t.Errorf("menu row %d missing (did the menu open?)", id)
		}
	}
}

// A key binding bypasses the menus, so each create/close/rename handler
// refuses on its own, with the flash, and leaves nothing half-done on screen
// (a split's placeholder, a rename field, a dialog).
func TestReadOnly_KeyBindingsRefusedWithFlash(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    tea.KeyPressMsg
		opened func(m Model) bool
	}{
		{"close pane", tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}, func(m Model) bool { return m.dialog == dialogConfirm }},
		{"close tab", tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt}, func(m Model) bool { return m.dialog == dialogConfirm }},
		{"rename tab", tea.KeyPressMsg{Code: tea.KeyF2}, func(m Model) bool { return m.renaming }},
		{"rename pane", tea.KeyPressMsg{Code: tea.KeyF2, Mod: tea.ModAlt}, func(m Model) bool { return m.renamingPane }},
		{"new tab", tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, func(m Model) bool { return m.dialog == dialogCreatePane }},
		{"split", tea.KeyPressMsg{Code: 'h', Mod: tea.ModAlt | tea.ModShift}, func(m Model) bool { return len(m.pendingSplit) > 0 }},
	} {
		for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
			t.Run(tc.name+"/"+rights, func(t *testing.T) {
				m, _ := readOnlyModel(t, rights)
				next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
				runCmdNoWait(cmd)
				m = next.(Model)
				next, cmd = m.Update(tc.key)
				runCmdNoWait(cmd)
				m = next.(Model)
				readOnly := rights == ipc.RightsReadOnly
				if got := tc.opened(m); got == readOnly {
					t.Fatalf("action took effect = %v on rights %q", got, rights)
				}
				if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
					t.Fatalf("read-only flash = %v on rights %q (flash %q)", flashed, rights, m.flashText)
				}
			})
		}
	}
}

func TestReadOnly_LocalSwitchRefused(t *testing.T) {
	m, conn := readOnlyModel(t, ipc.RightsReadOnly)
	cmd := m.switchTab(1)
	runCmdNoWait(cmd)
	if m.activeTabIdx() != 0 {
		t.Fatal("a read-only TUI switched tabs locally")
	}
	if got := actSent(conn); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// Control: a full destination switches.
func TestReadWrite_LocalSwitchAllowed(t *testing.T) {
	m, _ := readOnlyModel(t, ipc.RightsFull)
	cmd := m.switchTab(1)
	runCmdNoWait(cmd)
	if m.activeTabIdx() != 1 {
		t.Fatal("control: a full-rights TUI did not switch tabs")
	}
}

func followState(activeProject, activeTab string) WorkspaceStateMsg {
	return WorkspaceStateMsg{
		Dest: roDest, ActiveProject: activeProject, ActiveTab: activeTab,
		Projects: []ProjectInfo{
			{ID: "proj-a", Name: "A", TabIDs: []string{"t1"}, ActiveTab: "t1"},
			{ID: "proj-b", Name: "B", TabIDs: []string{"t2"}, ActiveTab: "t2"},
		},
		Tabs: []TabInfo{
			{ID: "t1", Name: "one", ProjectID: "proj-a", Panes: []string{"p1"}},
			{ID: "t2", Name: "two", ProjectID: "proj-b", Panes: []string{"p2"}},
		},
		Panes: []PaneInfo{{ID: "p1", TabID: "t1", Type: "terminal"}, {ID: "p2", TabID: "t2", Type: "terminal"}},
	}
}

// A viewer shows the daemon's active project and tab.
func TestReadOnly_FollowsDaemonActiveTab(t *testing.T) {
	for _, tc := range []struct {
		rights, wantProject string
	}{{ipc.RightsReadOnly, "proj-b"}, {ipc.RightsFull, "proj-a"}} {
		t.Run(tc.rights, func(t *testing.T) {
			m, _ := readOnlyModel(t, tc.rights)
			state := followState("proj-a", "t1")
			next, cmd := m.Update(state)
			runCmdNoWait(cmd)
			m = next.(Model)
			// Another client switches the daemon to proj-b / t2.
			state.ActiveProject, state.ActiveTab = "proj-b", "t2"
			next, cmd = m.Update(state)
			runCmdNoWait(cmd)
			m = next.(Model)
			if got := m.cur().ID; got != tc.wantProject {
				t.Fatalf("active project = %s, want %s", got, tc.wantProject)
			}
			if tc.rights == ipc.RightsReadOnly && m.activeTabModel().ID != "t2" {
				t.Fatalf("viewer active tab = %s, want t2", m.activeTabModel().ID)
			}
		})
	}
}

// The followed project is the one holding the daemon's ACTIVE TAB: an MCP
// switch_tab into another project moves the active tab and leaves the
// daemon's active project where it was.
func TestReadOnly_FollowsTheActiveTabsProject(t *testing.T) {
	for _, tc := range []struct {
		rights, wantProject string
	}{{ipc.RightsReadOnly, "proj-b"}, {ipc.RightsFull, "proj-a"}} {
		t.Run(tc.rights, func(t *testing.T) {
			m, _ := readOnlyModel(t, tc.rights)
			m = roUpdate(t, m, followState("proj-a", "t1"))
			// The daemon's active tab is now t2 (proj-b), its active project
			// still proj-a.
			m = roUpdate(t, m, followState("proj-a", "t2"))
			if got := m.cur().ID; got != tc.wantProject {
				t.Fatalf("active project = %s, want %s", got, tc.wantProject)
			}
			if tc.rights == ipc.RightsReadOnly && m.activeTabModel().ID != "t2" {
				t.Fatalf("viewer active tab = %s, want t2", m.activeTabModel().ID)
			}
		})
	}
}

// A daemon-side tab switch inside the viewer's project is followed as is: the
// typing guard (and its "switched by another client" flash) protects keys a
// viewer never sends. A full client arms it, as before.
func TestReadOnly_TabSwitchArmsNoTypingGuard(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			m, _ := readOnlyModel(t, rights)
			state := WorkspaceStateMsg{
				Dest: roDest, ActiveProject: "proj-a", ActiveTab: "t1",
				Projects: []ProjectInfo{{ID: "proj-a", Name: "A", TabIDs: []string{"t1", "t3"}, ActiveTab: "t1"}},
				Tabs: []TabInfo{
					{ID: "t1", Name: "one", ProjectID: "proj-a", Panes: []string{"p1"}},
					{ID: "t3", Name: "three", ProjectID: "proj-a", Panes: []string{"p3"}},
				},
				Panes: []PaneInfo{{ID: "p1", TabID: "t1", Type: "terminal"}, {ID: "p3", TabID: "t3", Type: "terminal"}},
			}
			next, cmd := m.Update(state)
			runCmdNoWait(cmd)
			m = next.(Model)
			state.ActiveTab, state.Projects[0].ActiveTab = "t3", "t3"
			next, cmd = m.Update(state)
			runCmdNoWait(cmd)
			m = next.(Model)
			if got := m.activeTabModel().ID; got != "t3" {
				t.Fatalf("active tab = %s, want t3", got)
			}
			if armed := !m.remoteSwitchAt.IsZero(); armed != (rights == ipc.RightsFull) {
				t.Fatalf("typing guard armed = %v on rights %q", armed, rights)
			}
		})
	}
}

// A viewer may still leave for another destination's project and come back:
// only a switch among the read-only daemon's own projects is refused, and
// coming back lands on the project that daemon has active, not the one the
// key happened to reach.
func TestReadOnly_ProjectSwitchAcrossDestinations(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			m, _ := readOnlyModel(t, rights)
			local := newFakeConn()
			t.Cleanup(func() { close(local.recv) })
			m.client.(*Router).Add("", local)
			next, cmd := m.Update(followState("proj-b", "t2"))
			runCmdNoWait(cmd)
			m = next.(Model)
			lt := NewTabModel("lt", "local")
			m.projects = append([]*ProjectModel{{ID: "proj-l", Name: "L", tabs: []*TabModel{lt}}}, m.projects...)
			m.activeProject = indexOfProject(m.projects, "proj-b")
			// projects: [proj-l, proj-a, proj-b]

			press := func(code rune) {
				next, cmd := m.Update(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: code})
				runCmdNoWait(cmd)
				m = next.(Model)
			}
			// proj-b → proj-a from the palette: within the daemon's own
			// projects, refused with the flash.
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
			for i, c := range m.paletteDisplay() {
				if c.action == palActSwitchProject && c.arg == "proj-a" {
					m.palette.cursor = i
				}
			}
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			want := "proj-a"
			if rights == ipc.RightsReadOnly {
				want = "proj-b"
			}
			if got := m.cur().ID; got != want {
				t.Fatalf("within the destination: active = %s, want %s", got, want)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != (rights == ipc.RightsReadOnly) {
				t.Fatalf("within the destination: read-only flash = %v (flash %q)", flashed, m.flashText)
			}
			// Previous from proj-b: a viewer steps PAST proj-a (same read-only
			// destination) to the local project instead of stopping on a
			// refusal.
			m.activeProject = indexOfProject(m.projects, "proj-b")
			m.flashText = ""
			press(tea.KeyLeft)
			want = "proj-a"
			if rights == ipc.RightsReadOnly {
				want = "proj-l"
			}
			if got := m.cur().ID; got != want {
				t.Fatalf("previous: active = %s, want %s", got, want)
			}
			if m.flashText == readOnlyFlash {
				t.Fatal("previous: a skip flashed a refusal")
			}
			m.activeProject = indexOfProject(m.projects, "proj-l")
			// Come back: next from proj-l reaches proj-a; a viewer lands on
			// the daemon's active proj-b instead.
			press(tea.KeyRight)
			want = "proj-a"
			if rights == ipc.RightsReadOnly {
				want = "proj-b"
			}
			if got := m.cur().ID; got != want {
				t.Fatalf("coming back: active = %s, want %s", got, want)
			}
		})
	}
}

// Next/previous with only the viewer's own destination's projects to step to:
// refused with the flash, nothing moves.
func TestReadOnly_ProjectCycleOnlyOwnDestination(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, _ := readOnlyModel(t, rights)
			m = roUpdate(t, m, followState("proj-b", "t2"))
			m.activeProject = indexOfProject(m.projects, "proj-b")
			m = roUpdate(t, m, tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: tea.KeyRight})
			want := "proj-a"
			if readOnly {
				want = "proj-b"
			}
			if got := m.cur().ID; got != want {
				t.Fatalf("active = %s, want %s", got, want)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
			}
		})
	}
}

// The rights come from the daemon on every login: a reconnect that logs in
// with a different level changes the mode.
func TestReadOnly_ReconnectReappliesRights(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{ipc.RightsFull, ipc.RightsReadOnly},
		{ipc.RightsReadOnly, ipc.RightsFull},
	} {
		t.Run(tc.from+"_to_"+tc.to, func(t *testing.T) {
			m, _ := readOnlyModel(t, tc.from)
			next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
			runCmdNoWait(cmd)
			m = next.(Model)

			fresh := newFakeConn()
			t.Cleanup(func() { close(fresh.recv) })
			m.SetRedialFunc(roDest, func(Client) (Client, error) {
				return &LoggedIn{Client: fresh, Rights: tc.to}, nil
			})
			next, _ = m.beginReconnect(roDest, errors.New("link lost"))
			m = next.(Model)
			link := m.linkOf(roDest)
			next, cmd = m.Update(redialTickMsg{gen: link.gen, dest: roDest, attempt: link.attempt})
			m = next.(Model)
			if cmd == nil {
				t.Fatal("setup: the armed tick started no dial")
			}
			next, cmd = m.Update(cmd())
			runCmdNoWait(cmd)
			m = next.(Model)
			if m.linkOf(roDest).active {
				t.Fatal("setup: the reconnect did not finish")
			}

			next, cmd = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			runCmdNoWait(cmd)
			m = next.(Model)
			readOnly := tc.to == ipc.RightsReadOnly
			if got := len(actSent(fresh)) == 0; got != readOnly {
				t.Fatalf("after reconnecting %s: sent %v", tc.to, actSent(fresh))
			}
			if got := strings.Contains(m.renderStatusBar(), "[read-only]"); got != readOnly {
				t.Fatalf("after reconnecting %s: [read-only] shown = %v", tc.to, got)
			}
			// The wrapper never reaches the router: the conn installed is the
			// dialled one itself, which the closer seam can type-assert.
			if c := m.client.(*Router).Conn(roDest); c != Client(fresh) {
				t.Fatalf("router holds %T, want the unwrapped conn", c)
			}
		})
	}
}

// A viewer's keystroke never reaches the input queue, not only the wire.
func TestReadOnly_KeystrokeNeverQueued(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			m, _ := readOnlyModel(t, rights)
			m.inputCh = make(chan paneInput, 4)
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			runCmdNoWait(cmd)
			m = next.(Model)
			want := 1
			if rights == ipc.RightsReadOnly {
				want = 0
			}
			if got := len(m.inputCh); got != want {
				t.Fatalf("queued %d input entries, want %d", got, want)
			}
		})
	}
}

// A destination connected from the New Project dialog takes its rights from
// that dial's login, like a reconnect does.
func TestReadOnly_RuntimeDialAppliesRights(t *testing.T) {
	const dest = "tcp:127.0.0.1:7979"
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir()) // adoptDest persists the destination
			m, _ := readOnlyModel(t, ipc.RightsFull)
			fresh := newFakeConn()
			t.Cleanup(func() { close(fresh.recv) })
			m.SetDialFunc(func(string) (Client, error) { return &LoggedIn{Client: fresh, Rights: rights}, nil })
			m.projectFormDialing = dest
			cmd := m.dialDest(dest)
			next, cmd := m.Update(cmd())
			runCmdNoWait(cmd)
			m = next.(Model)
			if got := m.destReadOnly(dest); got != (rights == ipc.RightsReadOnly) {
				t.Fatalf("destReadOnly = %v after a %s login", got, rights)
			}
			if c := m.client.(*Router).Conn(dest); c != Client(fresh) {
				t.Fatalf("router holds %T, want the unwrapped conn", c)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Every workspace mutation a viewer cannot send is refused locally, with the
// read-only flash, and leaves the screen as the daemon has it.
// ---------------------------------------------------------------------------

// twoPaneReadOnlyModel is readOnlyModel with tab-1 split left|right, sized.
func twoPaneReadOnlyModel(t *testing.T, rights string) (Model, *fakeConn) {
	t.Helper()
	m, conn := readOnlyModel(t, rights)
	p2 := NewPaneModel("pane-2", testRingBufSize)
	t.Cleanup(p2.Dispose)
	tab := m.projects[0].tabs[0]
	tab.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: tab.Root, Right: NewLeaf(p2)}
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmdNoWait(cmd)
	return next.(Model), conn
}

func roUpdate(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		runCmdNoWait(cmd)
		m = next.(Model)
	}
	return m
}

// Key family: moving and recolouring a tab.
func TestReadOnly_TabMoveAndColourKeysRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, conn := twoPaneReadOnlyModel(t, rights)
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModAlt | tea.ModShift})
			if moved := m.curTabs()[0].ID != "tab-1"; moved == readOnly {
				t.Fatalf("tab moved = %v on rights %q", moved, rights)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("move: read-only flash = %v (flash %q)", flashed, m.flashText)
			}
			m.flashText = ""
			before := m.activeTabModel().Color
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt})
			if changed := m.activeTabModel().Color != before; changed == readOnly {
				t.Fatalf("tab colour changed = %v on rights %q", changed, rights)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("colour: read-only flash = %v (flash %q)", flashed, m.flashText)
			}
			if sent := len(actSent(conn)) > 0; sent == readOnly {
				t.Fatalf("act messages sent = %v on rights %q: %v", sent, rights, actSent(conn))
			}
		})
	}
}

// Key family, the apply step itself: a bound layout key reaches
// applyTabArrangement with no menu or palette grey in front of it.
func TestReadOnly_LayoutKeyRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, _ := twoPaneReadOnlyModel(t, rights)
			press := layBind(t, &m, "tab.layout_rows")
			m = roUpdate(t, m, press)
			if arranged := m.activeTabModel().Root.Split == SplitVertical; arranged == readOnly {
				t.Fatalf("layout arranged = %v on rights %q", arranged, rights)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("read-only flash = %v (flash %q)", flashed, m.flashText)
			}
		})
	}
}

// Menu/palette family: a layout arrangement from the command palette.
func TestReadOnly_PaletteLayoutRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, _ := twoPaneReadOnlyModel(t, rights)
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt | tea.ModShift})
			if m.dialog != dialogCommandPalette {
				t.Fatal("setup: the palette did not open")
			}
			row := -1
			for i, c := range m.paletteDisplay() {
				if c.action == palActTabLayout && c.arg == "tab.layout_rows" {
					row = i
				}
			}
			if row < 0 {
				t.Fatal("setup: no Layout: rows row")
			}
			m.palette.cursor = row
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if arranged := m.activeTabModel().Root.Split == SplitVertical; arranged == readOnly {
				t.Fatalf("layout arranged = %v on rights %q", arranged, rights)
			}
		})
	}
}

// Mouse family: a tab-bar drag, an Alt+drag of a pane, a split-border drag.
func TestReadOnly_MouseDragsRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, conn := twoPaneReadOnlyModel(t, rights)

			// Tab bar: press on tab 1, drag past tab 2's middle, release.
			spans := m.tabSpans()
			if len(spans) < 2 {
				t.Fatal("setup: fewer than two tab spans")
			}
			off := m.projectSidebarWidth()
			m = roUpdate(t, m,
				tea.MouseClickMsg{X: off + spans[0].start + 1, Y: 0, Button: tea.MouseLeft},
				tea.MouseMotionMsg{X: off + spans[1].start + spans[1].width - 1, Y: 0, Button: tea.MouseLeft},
				tea.MouseReleaseMsg{X: off + spans[1].start + spans[1].width - 1, Y: 0, Button: tea.MouseLeft},
			)
			if moved := m.curTabs()[0].ID != "tab-1"; moved == readOnly {
				t.Fatalf("tab-bar drag moved the tab = %v on rights %q", moved, rights)
			}

			// Alt+press on a pane arms a pane drag.
			px, py := 0, 0
			for y := 2; y < m.height-2 && px == 0; y++ {
				for x := 0; x < m.width; x++ {
					if r := m.paneRectAt(x, y); r != nil && r.Pane != nil && r.Pane.ID == "pane-1" {
						px, py = x+2, y+2
						break
					}
				}
			}
			m = roUpdate(t, m, tea.MouseClickMsg{X: px, Y: py, Button: tea.MouseLeft, Mod: tea.ModAlt})
			if armed := m.paneDrag.active(); armed == readOnly {
				t.Fatalf("pane drag armed = %v on rights %q", armed, rights)
			}
			m = roUpdate(t, m, tea.MouseReleaseMsg{X: px, Y: py, Button: tea.MouseLeft})

			// Split border: the press must not arm the drag.
			bx, by := -1, 10
			for x := 0; x < m.width && bx < 0; x++ {
				if m.hitTestSplitBorder(x, by) != nil {
					bx = x
				}
			}
			if bx < 0 {
				t.Fatal("setup: no split border on row 10")
			}
			m.flashText = ""
			m = roUpdate(t, m, tea.MouseClickMsg{X: bx, Y: by, Button: tea.MouseLeft})
			if armed := m.splitDragNode != nil; armed == readOnly {
				t.Fatalf("split-border drag armed = %v on rights %q", armed, rights)
			}
			if flashed := m.flashText == readOnlyFlash; flashed != readOnly {
				t.Fatalf("split border: read-only flash = %v (flash %q)", flashed, m.flashText)
			}
			if readOnly {
				if got := actSent(conn); len(got) != 0 {
					t.Fatalf("a viewer's drags sent %v", got)
				}
			}
		})
	}
}

// A group change on a read-only destination's project is refused: the header
// menu greys Rename/Delete for a group holding one, and assigning one of its
// projects to a group leaves membership as it was.
func TestReadOnly_GroupChangesRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir())
			readOnly := rights == ipc.RightsReadOnly
			m, _ := readOnlyModel(t, rights)
			m.groups = projectGroups{Groups: []projectGroup{
				{Name: "G", Members: []groupMember{{Dest: roDest, ID: "proj-1"}}},
				{Name: "H"},
			}}
			m.sidebarOpen = true
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})

			// Right-click on G's header: Rename and Delete greyed for a viewer.
			m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowGroup), Button: tea.MouseRight})
			assertCtxRows(t, m.ctxMenu.items, !readOnly, ctxActRenameGroup, ctxActDeleteGroup)
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

			// Right-click on the project, then Enter on "Move to group…".
			m = roUpdate(t, m, tea.MouseClickMsg{X: 1, Y: sidebarRowY(t, m, sidebarRowProject), Button: tea.MouseRight})
			assertCtxRows(t, m.ctxMenu.items, !readOnly, ctxActGroupList)
			m.ctxMenu.cursor = ctxItemIndex(t, m.ctxMenu.items, ctxActGroupList)
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if !readOnly {
				// The list opened in place; choose H.
				for i, it := range m.ctxMenu.items {
					if it.id == ctxActSetGroup && it.groupName == "H" {
						m.ctxMenu.cursor = i
					}
				}
				m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if moved := m.groups.groupOf(roDest, "proj-1") == 1; moved == readOnly {
				t.Fatalf("project moved to group H = %v on rights %q", moved, rights)
			}
		})
	}
}

// A shared note opens view-only on a read-only destination.
func TestReadOnly_RemoteNoteIsViewOnly(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			m, _ := twoPaneReadOnlyModel(t, rights)
			m.sharedData = map[string]bool{roDest: true}
			m = roUpdate(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt})
			ed := m.notesEditor
			if ed == nil || !ed.Remote() {
				t.Fatal("setup: no remote notes editor opened")
			}
			ed.ApplyLoaded("hello\n", 3)
			if got := ed.editor.ReadOnly; got != (rights == ipc.RightsReadOnly) {
				t.Fatalf("editor ReadOnly = %v after load on rights %q", got, rights)
			}
		})
	}
}

// roCmdQuits runs cmd's tree (abandoning slow ticks) and reports whether any
// leaf answered tea.QuitMsg.
func roCmdQuits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.QuitMsg:
			return true
		case tea.BatchMsg:
			for _, c := range msg {
				if roCmdQuits(c) {
					return true
				}
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
	return false
}

func sentType(conn *fakeConn, typ string) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type == typ {
			return true
		}
	}
	return false
}

// Stop daemon is admin-class: a read-only or a standard token gets a greyed
// row, a refusal and a flash — no shutdown sent, and the TUI does not quit.
// Full stops the daemon and quits, as before.
func TestNoAdmin_StopDaemonRefused(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsStandard, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			canAdmin := rights == ipc.RightsFull
			m, conn := readOnlyModel(t, rights)
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyF1})
			if m.dialog != dialogAbout {
				t.Fatal("setup: F1 did not open About")
			}
			if greyed := strings.Contains(m.renderAboutDialog(), dialogSubtle.Render("Stop daemon")); greyed == canAdmin {
				t.Fatalf("Stop daemon row greyed = %v on rights %q", greyed, rights)
			}
			m.dialogCursor = aboutStopDaemonIndex
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if opened := m.dialog == dialogConfirm; opened != canAdmin {
				t.Fatalf("shutdown confirm opened = %v on rights %q", opened, rights)
			}
			// The confirm itself refuses too, for a confirm reached some other way.
			m.dialog, m.confirmKind = dialogConfirm, confirmKindShutdown
			m.flashText = ""
			next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			m = next.(Model)
			if quits := roCmdQuits(cmd); quits != canAdmin {
				t.Fatalf("TUI quit = %v on rights %q", quits, rights)
			}
			if sent := sentType(conn, ipc.MsgShutdown); sent != canAdmin {
				t.Fatalf("shutdown sent = %v on rights %q", sent, rights)
			}
			if flashed := m.flashText == noAdminFlash; flashed == canAdmin {
				t.Fatalf("no-admin flash = %v on rights %q (flash %q)", flashed, rights, m.flashText)
			}
		})
	}
}

// The overlay policy is sent only where it is accepted: not to a read-only or
// a standard token's daemon, whose refusals would be audit noise.
func TestNoAdmin_OverlayPolicyNotSent(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsStandard, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			m, conn := readOnlyModel(t, rights)
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			if sent := sentType(conn, ipc.MsgOverlayPolicy); sent != (rights == ipc.RightsFull) {
				t.Fatalf("overlay_policy sent = %v on rights %q", sent, rights)
			}
		})
	}
}

// Before the first broadcast — or on a daemon with nothing to report — there
// is no project, so the rights come from the destination this client started
// against: the tag shows, and Ctrl+T / Ctrl+N send nothing.
func TestReadOnly_NoProjectsYet(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			conn := newFakeConn()
			t.Cleanup(func() { close(conn.recv) })
			r := NewRouter(map[string]Client{roDest: conn})
			m := Model{
				cfg: config.Default(), client: r, tabDragFromIdx: -1, termFocused: true,
				notifications: NewNotificationCenter(30, 50),
			}
			m.initKeymap()
			m.SetHomeDest(roDest)
			m.SetDestRights(roDest, rights)
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			if tagged := strings.Contains(m.renderStatusBar(), "[read-only]"); tagged != readOnly {
				t.Fatalf("[read-only] shown = %v on rights %q", tagged, rights)
			}
			for _, key := range []tea.KeyPressMsg{{Code: 't', Mod: tea.ModCtrl}, {Code: 'n', Mod: tea.ModCtrl}} {
				m.dialog = dialogNone
				m = roUpdate(t, m, key)
				if opened := m.dialog == dialogCreatePane; opened == readOnly {
					t.Fatalf("%s opened the create dialog = %v on rights %q", key.String(), opened, rights)
				}
			}
			if readOnly {
				if got := actSent(conn); len(got) != 0 {
					t.Fatalf("a viewer with no projects sent act messages: %v", got)
				}
			}
		})
	}
}

// The router keys rights by the conn it actually picks: an unstamped send
// before the first broadcast (active dest "") reaches the sole conn, and that
// conn is read-only.
func TestRouter_SoleConnFallbackKeepsItsRights(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			conn := newFakeConn()
			defer close(conn.recv)
			r := NewRouter(map[string]Client{roDest: conn})
			r.SetDestRights(roDest, rights)
			act, _ := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{})
			_ = r.Send(act) // unstamped; the active dest is still ""
			if sent := sentType(conn, ipc.MsgCreateTab); sent != (rights == ipc.RightsFull) {
				t.Fatalf("create_tab reached the sole conn = %v on rights %q", sent, rights)
			}
		})
	}
}

// A notification for a pane in another of the viewer's tabs: Enter refuses,
// and leaves no history entry, no focus-mode toggle and the sidebar focused.
func TestReadOnly_NotificationJumpLeavesNoTrace(t *testing.T) {
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		t.Run(rights, func(t *testing.T) {
			readOnly := rights == ipc.RightsReadOnly
			m, _ := readOnlyModel(t, rights)
			p9 := NewPaneModel("pane-9", testRingBufSize)
			t.Cleanup(p9.Dispose)
			tab2 := m.projects[0].tabs[1]
			tab2.Root, tab2.ActivePane = NewLeaf(p9), "pane-9"
			m = roUpdate(t, m, tea.WindowSizeMsg{Width: 172, Height: 48})
			m.notifications.visible = true
			m.notifications.AddEvent(ipc.PaneEventPayload{ID: "e1", PaneID: "pane-9", TabID: "tab-2",
				Type: "command_complete", Title: "done", Severity: "info"})
			m.sidebarFocused = true
			m = roUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if moved := m.activeTabModel().ID == "tab-2"; moved == readOnly {
				t.Fatalf("jumped to tab-2 = %v on rights %q", moved, rights)
			}
			if readOnly {
				if len(m.paneHistory) != 0 || !m.sidebarFocused || m.activeTabModel().FocusMode() {
					t.Fatalf("a refused jump left history=%d sidebarFocused=%v focus=%v",
						len(m.paneHistory), m.sidebarFocused, m.activeTabModel().FocusMode())
				}
				if m.flashText != readOnlyFlash {
					t.Fatalf("flash = %q, want the read-only flash", m.flashText)
				}
			}
		})
	}
}

// Disconnecting a host forgets its rights and what its viewer followed, in
// the Model and in the router.
func TestReadOnly_DisconnectForgetsRights(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // forgetting a host rewrites config
	m, _ := readOnlyModel(t, ipc.RightsReadOnly)
	m = roUpdate(t, m, followState("proj-a", "t1"))
	m.confirmKind, m.confirmID, m.dialog = confirmKindDisconnectHost, roDest, dialogConfirm
	m = roUpdate(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.destReadOnly(roDest) || m.followProject[roDest] != "" {
		t.Fatalf("after disconnect: readOnly=%v follow=%q", m.destReadOnly(roDest), m.followProject[roDest])
	}
	r := m.client.(*Router)
	r.mu.RLock()
	_, kept := r.rights[roDest]
	r.mu.RUnlock()
	if kept {
		t.Fatal("after disconnect: the router still holds the destination's rights")
	}
}
