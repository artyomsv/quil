package tui

import (
	"errors"
	"strings"
	"testing"

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
			m.width, m.height = 172, 48

			greyed := 0
			for _, c := range m.buildPaletteCommands() {
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
			for _, c := range m.buildPaletteCommands() {
				switch c.action {
				case palActNewPane, palActClosePane, palActRenamePane, palActRenameTab, palActCloseTab:
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

			tab := m.activeTabModel()
			m.openCtxMenu(tab.ActivePaneModel(), 2, 2)
			assertCtxRows(t, m.ctxMenu.items, tc.enabled, ctxActRename, ctxActClose)
			m.closeCtxMenu()
			m.openTabCtxMenu(tab, 2, 2)
			assertCtxRows(t, m.ctxMenu.items, tc.enabled, ctxActRenameTab)
			m.closeCtxMenu()
			m.openProjectCtxMenu(m.cur(), 2, 2)
			assertCtxRows(t, m.ctxMenu.items, tc.enabled, ctxActRenameProject)
			// Client-side only: enabled whatever the rights.
			assertCtxRows(t, m.ctxMenu.items, true, ctxActDisconnectHost)
		})
	}
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
			// proj-b → proj-a: within the daemon's own projects.
			press(tea.KeyLeft)
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
			// Leave for the local project: always allowed.
			m.activeProject = indexOfProject(m.projects, "proj-a")
			press(tea.KeyLeft)
			if got := m.cur().ID; got != "proj-l" {
				t.Fatalf("leaving: active = %s, want proj-l", got)
			}
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
