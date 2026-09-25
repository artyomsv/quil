package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Multi-client sync, follower rendering (spec §5). A follower shows each pane
// at the size the MASTER chose — its VT takes the daemon's cols/rows, never its
// own box — and cuts or pads that grid into the box it has. Every decision is
// driven through Model.Update: the follower flag reaches the pane only through
// a broadcast, so a test that set it directly could pass against a syncPaneMeta
// that never copies it.

// followerFixture lays out paneIDs in one tab with NO master (so the rects
// settle exactly as they do today), then applies a second broadcast naming
// `master` as the size master and reporting, per pane, the cols/rows sizeFn
// derives from that pane's inner box. mutate may adjust the second broadcast
// before it is applied. The returned conn has an empty send log.
func followerFixture(t *testing.T, master string, paneIDs []string,
	sizeFn func(id string, innerW, innerH int) (cols, rows int),
	mutate func(*WorkspaceStateMsg)) (Model, *fakeConn) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	m, conn := tinyTermModel(t)
	m.SetClientID("me")
	m.notifications = NewNotificationCenter(30, 50) // the mouse and View paths read it
	// Receive() must not park the listen command each broadcast re-arms.
	close(conn.recv)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 124, Height: 44})
	runCmd(cmd)
	m = next.(Model)

	st := WorkspaceStateMsg{
		Dest: "", Clients: 2,
		ActiveProject: "proj-1", ActiveTab: "tab-1",
		Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: paneIDs}},
	}
	for _, id := range paneIDs {
		st.Panes = append(st.Panes, PaneInfo{ID: id, TabID: "tab-1", Type: "terminal"})
	}
	next, cmd = m.Update(st)
	runCmd(cmd)
	m = next.(Model)

	st.SizeMaster = master
	for i := range st.Panes {
		p := paneByID(t, m, st.Panes[i].ID)
		c, r := sizeFn(p.ID, p.Width-2, p.Height-2)
		st.Panes[i].Cols, st.Panes[i].Rows = uint16(c), uint16(r)
	}
	if mutate != nil {
		mutate(&st)
	}
	next, cmd = m.Update(st)
	runCmd(cmd)
	m = next.(Model)
	clearSent(conn)
	return m, conn
}

// paneByID resolves a tree pane or an overlay pane of the fixture's model.
func paneByID(t *testing.T, m Model, id string) *PaneModel {
	t.Helper()
	for _, tab := range m.allTabs() {
		if tab.overlayPane != nil && tab.overlayPane.ID == id {
			return tab.overlayPane
		}
		if tab.Root != nil {
			if leaf := tab.Root.FindLeaf(id); leaf != nil {
				return leaf.Pane
			}
		}
	}
	t.Fatalf("pane %s not found", id)
	return nil
}

func fixedSize(cols, rows int) func(string, int, int) (int, int) {
	return func(string, int, int) (int, int) { return cols, rows }
}

func vtSize(p *PaneModel) (int, int) { return p.vt.Width(), p.vt.Height() }

// feedOutput delivers bytes to a pane through Update, as the listener would.
func feedOutput(t *testing.T, m Model, paneID, data string) Model {
	t.Helper()
	next, cmd := m.Update(PaneOutputMsg{PaneID: paneID, Data: []byte(data)})
	_ = cmd // settle ticks and the listen re-arm are irrelevant here
	return next.(Model)
}

// numberedRows writes n rows "L00".."L<n-1>", the last without a newline so a
// screen of exactly n rows does not scroll.
func numberedRows(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "L%02d", i)
	}
	return b.String()
}

// assertExactBox checks every rendered line of a pane is exactly the pane's
// width and that there are exactly Height lines — a follower's grid is never
// allowed to widen, narrow or lengthen the box it is drawn in.
func assertExactBox(t *testing.T, p *PaneModel, view string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != p.Height {
		t.Errorf("rendered %d lines, want %d (the pane height)", len(lines), p.Height)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != p.Width {
			t.Errorf("line %d width = %d, want %d: %q", i, w, p.Width, stripANSI(l))
		}
	}
}

func TestFollower_VTTakesDaemonSize(t *testing.T) {
	m, _ := followerFixture(t, "other", []string{"pane-1"}, fixedSize(200, 50), nil)
	p := paneByID(t, m, "pane-1")
	if p.Width-2 >= 200 || p.Height-2 >= 50 {
		t.Fatalf("setup: box %dx%d must be smaller than the daemon size", p.Width, p.Height)
	}
	if c, r := vtSize(p); c != 200 || r != 50 {
		t.Fatalf("follower VT = %dx%d, want the daemon's 200x50", c, r)
	}

	next, _ := m.Update(paneSizesMsg{dest: "", sizes: []ipc.ResizePanePayload{{PaneID: "pane-1", Cols: 180, Rows: 45}}})
	m = next.(Model)
	if c, r := vtSize(paneByID(t, m, "pane-1")); c != 180 || r != 45 {
		t.Fatalf("after pane_sizes VT = %dx%d, want 180x45", c, r)
	}
}

// A pane_sizes frame for a DIFFERENT destination must not touch a pane that
// merely shares an id — sizes describe one daemon's panes.
func TestFollower_PaneSizesMsgScopedToDest(t *testing.T) {
	m, _ := followerFixture(t, "other", []string{"pane-1"}, fixedSize(200, 50), nil)
	next, _ := m.Update(paneSizesMsg{dest: "remote-a", sizes: []ipc.ResizePanePayload{{PaneID: "pane-1", Cols: 180, Rows: 45}}})
	m = next.(Model)
	if c, r := vtSize(paneByID(t, m, "pane-1")); c != 200 || r != 50 {
		t.Fatalf("VT = %dx%d, want 200x50 — another destination's sizes applied", c, r)
	}
}

// The listener decodes MsgPaneSizes into a paneSizesMsg stamped with the
// frame's origin, and the Update arm renders (it changes the frame).
func TestFollower_ListenerDecodesPaneSizes(t *testing.T) {
	m, conn := tinyTermModel(t)
	msg, err := ipc.NewMessage(ipc.MsgPaneSizes, ipc.PaneSizesPayload{
		Panes: []ipc.ResizePanePayload{{PaneID: "pane-1", Cols: 90, Rows: 30}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg.Origin = "host-b"
	conn.recv <- msg
	got := m.listenForMessages()()
	ps, ok := got.(paneSizesMsg)
	if !ok {
		t.Fatalf("listener returned %T, want paneSizesMsg", got)
	}
	if ps.dest != "host-b" || len(ps.sizes) != 1 || ps.sizes[0].Cols != 90 || ps.sizes[0].Rows != 30 {
		t.Fatalf("decoded %+v", ps)
	}
	next, _ := m.Update(ps)
	if next.(Model).skipRender {
		t.Fatal("a pane_sizes frame changes the frame and must not be skipRender")
	}
}

// §4.1 / 8a: the size arrives BEFORE the repaint, so the repaint must land in
// the new-sized VT. 185 cells in a 180-column grid wrap to row 1 col 5; in the
// old 200-column grid they would not wrap at all.
func TestFollower_PaneSizesMsgResizesBeforeOutput(t *testing.T) {
	m, _ := followerFixture(t, "other", []string{"pane-1"}, fixedSize(200, 50), nil)
	next, _ := m.Update(paneSizesMsg{dest: "", sizes: []ipc.ResizePanePayload{{PaneID: "pane-1", Cols: 180, Rows: 45}}})
	m = next.(Model)
	m = feedOutput(t, m, "pane-1", strings.Repeat("x", 185))
	pos := paneByID(t, m, "pane-1").vt.CursorPosition()
	if pos.X != 5 || pos.Y != 1 {
		t.Fatalf("cursor at (%d,%d), want (5,1) — output landed in a VT of the old width", pos.X, pos.Y)
	}
}

func TestFollower_RenderTooWideCropsLeftWithMarker(t *testing.T) {
	var innerW int
	m, _ := followerFixture(t, "other", []string{"pane-1"}, func(_ string, w, h int) (int, int) {
		innerW = w
		return w + 30, h
	}, nil)
	row := strings.Repeat("a", innerW) + "BBBB"
	m = feedOutput(t, m, "pane-1", row)
	p := paneByID(t, m, "pane-1")
	view := p.View()
	lines := strings.Split(view, "\n")
	first := stripANSI(lines[1])
	if !strings.Contains(first, strings.Repeat("a", innerW)) || strings.Contains(first, "B") {
		t.Errorf("first row %q: want the LEFT %d columns only", first, innerW)
	}
	top := []rune(stripANSI(lines[0]))
	if top[len(top)-1] != '…' {
		t.Errorf("top border %q: want a … at the right end for a width cut", string(top))
	}
	if top[0] == '…' {
		t.Errorf("top border %q: no height cut, so no … at the left end", string(top))
	}
	assertExactBox(t, p, view)
}

func TestFollower_RenderTooTallShowsBottomRowsWithMarker(t *testing.T) {
	var innerH int
	m, _ := followerFixture(t, "other", []string{"pane-1"}, func(_ string, w, h int) (int, int) {
		innerH = h
		return w, h + 10
	}, nil)
	m = feedOutput(t, m, "pane-1", numberedRows(innerH+10))
	p := paneByID(t, m, "pane-1")
	view := p.View()
	lines := strings.Split(view, "\n")
	if got := stripANSI(lines[1]); !strings.Contains(got, "L10") {
		t.Errorf("first visible row %q: want L10 (bottom-anchored, 10 rows cut)", got)
	}
	if got := stripANSI(lines[innerH]); !strings.Contains(got, fmt.Sprintf("L%02d", innerH+9)) {
		t.Errorf("last visible row %q: want L%02d", got, innerH+9)
	}
	top := []rune(stripANSI(lines[0]))
	if top[0] != '…' {
		t.Errorf("top border %q: want a … at the left end for a height cut", string(top))
	}
	if top[len(top)-1] == '…' {
		t.Errorf("top border %q: no width cut, so no … at the right end", string(top))
	}
	assertExactBox(t, p, view)
}

func TestFollower_RenderSmallerIsPadded(t *testing.T) {
	m, _ := followerFixture(t, "other", []string{"pane-1"}, fixedSize(20, 5), nil)
	m = feedOutput(t, m, "pane-1", numberedRows(5))
	p := paneByID(t, m, "pane-1")
	if p.previewMode() {
		t.Fatal("a grid that fits must use the native renderer, not the preview")
	}
	view := p.View()
	lines := strings.Split(view, "\n")
	if got := stripANSI(lines[1]); !strings.HasPrefix(got, "│L00") {
		t.Errorf("first row %q: want the grid drawn top-left", got)
	}
	for i := 6; i < len(lines)-1; i++ {
		if got := strings.Trim(stripANSI(lines[i]), "│ "); got != "" {
			t.Errorf("row %d %q: want blank padding below the grid", i, got)
		}
	}
	top := stripANSI(lines[0])
	if strings.Contains(top, "…") {
		t.Errorf("top border %q: nothing is cut, so no marker", top)
	}
	assertExactBox(t, p, view)
}

func TestFollower_RenderEveryRowExactWidth(t *testing.T) {
	cases := map[string]func(string, int, int) (int, int){
		"wide":    func(_ string, w, h int) (int, int) { return w + 40, h },
		"tall":    func(_ string, w, h int) (int, int) { return w, h + 7 },
		"both":    func(_ string, w, h int) (int, int) { return w + 40, h + 7 },
		"smaller": func(_ string, w, h int) (int, int) { return w / 2, h / 2 },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := followerFixture(t, "other", []string{"pane-1"}, fn, nil)
			p := paneByID(t, m, "pane-1")
			c, r := vtSize(p)
			var b strings.Builder
			for i := 0; i < r; i++ {
				if i > 0 {
					b.WriteString("\r\n")
				}
				b.WriteString(strings.Repeat(string(rune('a'+i%26)), c))
			}
			m = feedOutput(t, m, "pane-1", b.String())
			assertExactBox(t, p, p.View())
			// Scrolled back too: the scrollbar column must not widen a row.
			p.ScrollUp(2)
			assertExactBox(t, p, p.View())
		})
	}
}

// Spec §5.2: scrollback sits ABOVE the visible rows, so the wheel first
// reveals the screen rows the box cut, then the history.
func TestFollower_WheelRevealsCutRowsFirst(t *testing.T) {
	var innerH int
	m, _ := followerFixture(t, "other", []string{"pane-1"}, func(_ string, w, h int) (int, int) {
		innerH = h
		return w, h + 10
	}, nil)
	m = feedOutput(t, m, "pane-1", numberedRows(innerH+10))
	rect := m.activePaneRect()
	if rect == nil {
		t.Fatal("no active pane rect")
	}
	next, _ := m.Update(tea.MouseWheelMsg{X: rect.OX + 3, Y: rect.OY + 3, Button: tea.MouseWheelUp})
	m = next.(Model)
	p := paneByID(t, m, "pane-1")
	lines := strings.Split(p.View(), "\n")
	lines = lines[1:] // drop the top border
	notch := m.cfg.UI.MouseScrollLines
	if notch < 1 {
		notch = 3 // the handler's own default
	}
	want := fmt.Sprintf("L%02d", 10-notch)
	if got := stripANSI(lines[0]); !strings.Contains(got, want) {
		t.Errorf("after one notch the top row is %q, want %s (a cut screen row)", got, want)
	}
}

// wheelForwarded returns the bytes the input queue received.
func wheelForwarded(m Model) string {
	var got strings.Builder
	for {
		select {
		case in := <-m.inputCh:
			got.Write(in.data)
		default:
			return got.String()
		}
	}
}

func trackingMutate(st *WorkspaceStateMsg) {
	for i := range st.Panes {
		st.Panes[i].MouseTracking, st.Panes[i].MouseSGR = true, true
	}
}

// Spec §5.3: box row r of a too-tall follower is grid row r + (vtH - innerH).
func TestFollower_WheelForwardTranslatedToGridRow(t *testing.T) {
	var innerH int
	m, _ := followerFixture(t, "other", []string{"pane-1"}, func(_ string, w, h int) (int, int) {
		innerH = h
		return w, h + 10
	}, trackingMutate)
	m.inputCh = make(chan paneInput, inputForwardBuffer)
	rect := m.activePaneRect()
	relX, relY := 4, 3
	next, _ := m.Update(tea.MouseWheelMsg{X: rect.OX + 1 + relX, Y: rect.OY + 1 + relY, Button: tea.MouseWheelUp})
	m = next.(Model)
	want := fmt.Sprintf("\x1b[<64;%d;%dM", relX+1, relY+10+1)
	if got := wheelForwarded(m); got != want {
		t.Fatalf("forwarded %q, want %q (box row %d + %d cut rows)", got, want, relY, 10)
	}
	_ = innerH
}

// Spec §5.3: a position in the padding of a grid smaller than the box sends
// nothing — there is no grid cell there for the app to receive.
func TestFollower_WheelInPaddingNotForwarded(t *testing.T) {
	m, _ := followerFixture(t, "other", []string{"pane-1"}, fixedSize(20, 5), trackingMutate)
	m.inputCh = make(chan paneInput, inputForwardBuffer)
	rect := m.activePaneRect()
	for _, pos := range [][2]int{{25, 2}, {3, 8}} {
		next, _ := m.Update(tea.MouseWheelMsg{X: rect.OX + 1 + pos[0], Y: rect.OY + 1 + pos[1], Button: tea.MouseWheelUp})
		m = next.(Model)
		if got := wheelForwarded(m); got != "" {
			t.Errorf("wheel at padding (%d,%d) forwarded %q, want nothing", pos[0], pos[1], got)
		}
	}
	// Inside the grid it is forwarded unchanged: the grid is top-left.
	next, _ := m.Update(tea.MouseWheelMsg{X: rect.OX + 1 + 3, Y: rect.OY + 1 + 2, Button: tea.MouseWheelUp})
	m = next.(Model)
	if got, want := wheelForwarded(m), "\x1b[<64;4;3M"; got != want {
		t.Errorf("wheel inside the grid forwarded %q, want %q", got, want)
	}
}

// Spec 8a: the two sizing sites outside the layout walk — sizePaneFull for
// focus mode and for an overlay — take the daemon size too.
func TestFollower_OverlayAndFocusModeUseDaemonSize(t *testing.T) {
	sizes := map[string][2]int{"pane-1": {50, 12}, "pane-2": {52, 13}, "ov-1": {70, 25}}
	m, _ := followerFixture(t, "other", []string{"pane-1", "pane-2"},
		func(id string, _, _ int) (int, int) { return sizes[id][0], sizes[id][1] },
		nil)

	next, cmd := m.toggleFocusForActiveTab()
	runCmd(cmd)
	m = next.(Model)
	tab := m.activeTabModel()
	if !tab.FocusMode() {
		t.Fatal("setup: focus mode did not engage")
	}
	active := tab.ActivePaneModel()
	if c, r := vtSize(active); c != sizes[active.ID][0] || r != sizes[active.ID][1] {
		t.Errorf("focus-mode VT = %dx%d, want the daemon's %v (box is %dx%d)", c, r, sizes[active.ID], active.Width, active.Height)
	}
	next, cmd = m.toggleFocusForActiveTab()
	runCmd(cmd)
	m = next.(Model)

	// Overlay: announced by this client's Alt+G (pendingOverlayShow), so it
	// arrives visible and is sized by sizePaneFull.
	m.pendingOverlayShow = map[string]bool{"tab-1": true}
	st := WorkspaceStateMsg{
		Dest: "", SizeMaster: "other", Clients: 2,
		ActiveProject: "proj-1", ActiveTab: "tab-1",
		Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1", "pane-2", "ov-1"}}},
	}
	for _, id := range []string{"pane-1", "pane-2", "ov-1"} {
		st.Panes = append(st.Panes, PaneInfo{ID: id, TabID: "tab-1", Type: "terminal",
			Overlay: id == "ov-1", Cols: uint16(sizes[id][0]), Rows: uint16(sizes[id][1])})
	}
	next, cmd = m.Update(st)
	runCmd(cmd)
	m = next.(Model)
	tab = m.activeTabModel()
	if !tab.overlayVisible || tab.overlayPane == nil {
		t.Fatal("setup: overlay not visible")
	}
	if c, r := vtSize(tab.overlayPane); c != 70 || r != 25 {
		t.Errorf("overlay VT = %dx%d, want the daemon's 70x25 (box is %dx%d)", c, r, tab.overlayPane.Width, tab.overlayPane.Height)
	}
}

// runCmdNoWait runs cmd like runCmd but abandons a leaf that has not returned
// within a short budget: a tea.Tick sleeps its whole interval (the notes
// autosave tick is seconds), and nothing a tick produces matters here. Every
// send under test happens synchronously inside its own leaf.
func runCmdNoWait(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				runCmdNoWait(c)
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// Review Focus 5: a follower's own rect changes — focus mode, notes mode, the
// notification sidebar, the project sidebar, its window — neither resize its
// VTs nor send a single resize frame.
func TestFollower_LocalRectChangesNeverResizeTheVT(t *testing.T) {
	sizes := map[string][2]int{"pane-1": {61, 17}, "pane-2": {63, 11}}
	m, conn := followerFixture(t, "other", []string{"pane-1", "pane-2"},
		func(id string, _, _ int) (int, int) { return sizes[id][0], sizes[id][1] },
		nil)
	check := func(step string) {
		t.Helper()
		for id, want := range sizes {
			if c, r := vtSize(paneByID(t, m, id)); c != want[0] || r != want[1] {
				t.Errorf("%s: %s VT = %dx%d, want %dx%d", step, id, c, r, want[0], want[1])
			}
		}
		if n := countResizes(t, conn); n != 0 {
			t.Errorf("%s: %d resize(s) sent, want 0", step, n)
		}
	}

	next, cmd := m.toggleFocusForActiveTab()
	runCmd(cmd)
	m = next.(Model)
	m.View()
	check("focus on")
	next, cmd = m.toggleFocusForActiveTab()
	runCmd(cmd)
	m = next.(Model)
	m.View()
	check("focus off")

	next, cmd = m.toggleNotesMode()
	runCmdNoWait(cmd)
	m = next.(Model)
	if !m.notesMode {
		t.Fatal("setup: notes mode did not open")
	}
	m.View()
	check("notes on")
	next, cmd = m.toggleNotesMode()
	runCmdNoWait(cmd)
	m = next.(Model)
	m.View()
	check("notes off")

	m.notifications.visible = true
	m.View()
	check("notification sidebar")
	m.notifications.visible = false

	next, cmd = m.toggleProjectSidebar()
	runCmd(cmd)
	m = next.(Model)
	m.View()
	check("project sidebar")

	next, _ = m.Update(tea.WindowSizeMsg{Width: 150, Height: 50})
	m = next.(Model)
	next, cmd = m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(cmd)
	m = next.(Model)
	m.View()
	check("window resize")
}

// A master's pane renders exactly as before this feature: its VT follows its
// own rect (paneVTSize), whatever cols/rows the broadcast carries, and its
// frame is byte-identical to a pane sized the pre-change way.
func TestMaster_RenderUnchanged(t *testing.T) {
	m, _ := followerFixture(t, "me", []string{"pane-1"}, fixedSize(200, 50), nil)
	out := numberedRows(8) + "\r\n" + strings.Repeat("w", 300)
	m = feedOutput(t, m, "pane-1", out)
	p := paneByID(t, m, "pane-1")
	wantC, wantR := paneVTSize(false, 0, p.Width, p.Height, p.NativeW, 0, 0)
	if c, r := vtSize(p); c != wantC || r != wantR {
		t.Fatalf("master VT = %dx%d, want its own rect's %dx%d", c, r, wantC, wantR)
	}

	ref := NewPaneModel("pane-1", testRingBufSize)
	t.Cleanup(ref.Dispose)
	ref.Type, ref.Name, ref.CWD = p.Type, p.Name, p.CWD
	ref.Active, ref.liveOutputSeen, ref.unseen = p.Active, p.liveOutputSeen, p.unseen
	ref.ghost, ref.resuming, ref.preparing = p.ghost, p.resuming, p.preparing
	ref.Width, ref.Height = p.Width, p.Height
	ref.ResizeVT(wantC, wantR)
	ref.AppendOutput([]byte(out))
	if got, want := p.View(), ref.View(); got != want {
		t.Fatalf("master frame differs from the pre-change rendering:\n got: %q\nwant: %q", got, want)
	}
}
