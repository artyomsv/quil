package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/keymap"
)

// Multi-client sync: TUI client identity, role, resize gates, Take control.
//
// isFollower(dest) = sizeMaster[dest] != "" && sizeMaster[dest] != clientID.
// resizeAllPanes, diffResizes and overlayResizeCmd are the three producers of
// pane resizes, and each gates on it — see model.go/overlay.go for the gates
// themselves. These tests drive Model.Update (or the literal Update dispatch
// target, e.g. finishSplitDrag), never the follower gate directly.

// sawResizePanes reports whether any MsgResizePanes batch reached the wire.
func sawResizePanes(conn *fakeConn) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type == ipc.MsgResizePanes {
			return true
		}
	}
	return false
}

// sawClientGeometry reports whether a MsgClientGeometry reached the wire.
func sawClientGeometry(conn *fakeConn) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type == ipc.MsgClientGeometry {
			return true
		}
	}
	return false
}

// followerWorkspaceState builds the smallest broadcast that makes dest "" a
// follower of "other-client", differing in reported size from what a
// non-follower diff would compute (0x0 versus a real pane rect) — so a model
// that ignored the follower gate would have something to send.
func followerWorkspaceState() WorkspaceStateMsg {
	return WorkspaceStateMsg{
		Dest: "", SizeMaster: "other-client", Clients: 2,
		ActiveProject: "proj-1", ActiveTab: "tab-1",
		Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1"}}},
		Panes:    []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}},
	}
}

// TestFollower_SendsNoResizeOnBroadcast is the first of the three mutation
// checks: a broadcast reporting a size master other than this client, with a
// size that genuinely differs from what this client would compute, must
// produce zero resize sends.
func TestFollower_SendsNoResizeOnBroadcast(t *testing.T) {
	t.Parallel()
	m, conn := tinyTermModel(t)
	m.SetClientID("me")

	// A healthy first size, so the pane has a real rect and a non-follower
	// diff would find something to send.
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd)
	m = next.(Model)
	clearSent(conn)

	// Receive() must not park the listen command the broadcast re-arms.
	close(conn.recv)
	_, cmd = m.Update(followerWorkspaceState())
	runCmd(cmd)

	if n := countResizes(t, conn); n != 0 {
		t.Errorf("resize count = %d, want 0 — a follower must never resize a "+
			"pane the master owns", n)
	}
}

// TestFollower_SendsNoResizeOnWindowResize: second mutation check. The client
// still reports its OWN window size (client_geometry) — that is what lets the
// daemon notice a master that became unpaintable — but sends no pane resize.
func TestFollower_SendsNoResizeOnWindowResize(t *testing.T) {
	t.Parallel()
	m, conn := tinyTermModel(t)
	m.SetClientID("me")

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd)
	m = next.(Model)

	// Make this client a follower without running the broadcast's own cmds
	// (which would re-arm the listen loop against an empty, unclosed channel).
	m.sizeMaster = map[string]string{"": "other-client"}
	m.clientCount = map[string]int{"": 2}
	clearSent(conn)

	// The tea.Tick is deliberately not run — the arm under test is the
	// resizeTickMsg one, delivered here directly through Update, matching
	// every other debounced-resize test in this package (tinyterm_test.go).
	next, _ = m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	m = next.(Model)
	_, tickCmd := m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(tickCmd)

	if n := countResizes(t, conn); n != 0 {
		t.Errorf("resize count = %d, want 0 — a follower must not resize a "+
			"pane on its own window change", n)
	}
	if !sawClientGeometry(conn) {
		t.Error("a follower must still report its own window size via " +
			"client_geometry, or the daemon can never notice it became " +
			"unpaintable and hand off")
	}
}

// TestFollower_SendsNoResizeOnSplitDragRelease: third mutation check, for
// diffResizes/resizeAllPanes' shared call path — finishSplitDrag is the exact
// method Update's MouseReleaseMsg case dispatches a split-border release to.
func TestFollower_SendsNoResizeOnSplitDragRelease(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.SetClientID("me")
	m.sizeMaster = map[string]string{"": "other-client"}
	m.clientCount = map[string]int{"": 2}
	conn := newFakeConn()
	m.client = conn

	// Arm and drag, exactly as TestModel_FinishSplitDrag_CommitsToDaemon does
	// for the master case.
	hit := m.hitTestSplitBorder(50, 10)
	if hit == nil {
		t.Fatal("setup: no split border hit at (50,10)")
	}
	m.splitDragNode = hit.Node
	m.splitDragRect = *hit
	m.dragSplitBorder(70, 10)

	cmd := m.finishSplitDrag()
	if cmd == nil {
		t.Fatal("finishSplitDrag returned nil — the layout commit must still happen")
	}
	runCmd(cmd)

	if sawResizePanes(conn) {
		t.Error("a follower's split-drag release must not resize any pane")
	}
}

// TestFollower_OverlayResizeGated: the fourth site, and the one no tree walk
// reaches — an overlay pane sits outside the layout tree, so this sweep
// (resizeTickMsg) is its only resize producer.
func TestFollower_OverlayResizeGated(t *testing.T) {
	t.Parallel()
	m, conn := tinyTermModel(t)
	m.SetClientID("me")

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd)
	m = next.(Model)

	overlay := NewPaneModel("overlay-1", testRingBufSize)
	t.Cleanup(overlay.Dispose)
	tab := m.projects[0].tabs[0]
	tab.overlayPane = overlay
	tab.overlayVisible = true
	m.sizeMaster = map[string]string{"": "other-client"}
	m.clientCount = map[string]int{"": 2}
	clearSent(conn)

	next, _ = m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	m = next.(Model)
	_, tickCmd := m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(tickCmd)

	if sawResizeFor(t, conn, "overlay-1") {
		t.Error("a follower must not resize its overlay pane either")
	}
}

// TestMaster_ResizeAllPanesSendsOneBatchPerDest is Review Focus 2: a resize
// burst across many panes must not put one must-deliver frame per pane on a
// follower's queue. 40 panes, one destination, one MsgResizePanes frame.
func TestMaster_ResizeAllPanesSendsOneBatchPerDest(t *testing.T) {
	t.Parallel()
	const n = 40
	tabs := make([]*TabModel, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("pane-%d", i)
		tab := NewTabModel(fmt.Sprintf("tab-%d", i), fmt.Sprintf("T%d", i))
		pane := NewPaneModel(id, testRingBufSize)
		t.Cleanup(pane.Dispose)
		tab.Root = NewLeaf(pane)
		tab.ActivePane = id
		tab.Resize(80, 24)
		tabs[i] = tab
	}
	conn := newFakeConn()
	m := Model{
		client: conn, sized: true, width: 172, height: 48,
		tabDragFromIdx: -1,
		projects:       []*ProjectModel{{ID: "proj-1", Name: "Default", tabs: tabs}},
	}

	cmd := m.resizeAllPanes()
	if cmd == nil {
		t.Fatal("resizeAllPanes returned nil")
	}
	runCmd(cmd)

	var frames, panes int
	for _, msg := range conn.sent {
		if msg.Type != ipc.MsgResizePanes {
			continue
		}
		frames++
		var p ipc.ResizePanesPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode resize_panes: %v", err)
		}
		panes += len(p.Panes)
	}
	if frames != 1 {
		t.Errorf("resize_panes frame count = %d, want 1 — a resize burst must "+
			"not put one must-deliver frame per pane on the wire", frames)
	}
	if panes != n {
		t.Errorf("panes carried across all frames = %d, want %d", panes, n)
	}
}

// TestBecomingMaster_ResendsSizes: on the broadcast that hands this client
// the master role, every pane must be resized at once — even one diffResizes
// alone would have suppressed, because the reported size already agrees with
// what this client would compute AND sizedOnce already says "already sent".
func TestBecomingMaster_ResendsSizes(t *testing.T) {
	t.Parallel()
	m, conn := tinyTermModel(t)
	m.SetClientID("me")

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd)
	m = next.(Model)

	tab := m.projects[0].tabs[0]
	pane := tab.Leaves()[0]
	cols, rows := paneVTSize(pane.WideCanvas, pane.MinNativeCols,
		pane.Width, pane.Height, pane.NativeW, tab.CanvasW, tab.CanvasH)

	// A follower that already sent this exact size once before (e.g. from an
	// earlier mastership) — diffResizes' own diff would find nothing to send.
	m.sizeMaster = map[string]string{"": "other-client"}
	m.sizedOnce = map[string]bool{sizedKey("", "pane-1"): true}
	clearSent(conn)

	close(conn.recv)
	_, cmd = m.Update(WorkspaceStateMsg{
		Dest: "", SizeMaster: "me", Clients: 2,
		ActiveProject: "proj-1", ActiveTab: "tab-1",
		Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1"}}},
		Panes:    []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal", Cols: uint16(cols), Rows: uint16(rows)}},
	})
	runCmd(cmd)

	if !sawResizeFor(t, conn, "pane-1") {
		t.Error("becoming the size master must resize every pane at once, " +
			"even one diffResizes alone would have suppressed")
	}
}

// TestAttach_CarriesClientID: attachMessage must carry the process-minted id
// on every attach and reattach.
func TestAttach_CarriesClientID(t *testing.T) {
	t.Parallel()
	m := Model{cfg: config.Default(), width: 172, height: 48}
	m.SetClientID("my-client-id")

	var p ipc.AttachPayload
	if err := m.attachMessage("").DecodePayload(&p); err != nil {
		t.Fatalf("decode attach payload: %v", err)
	}
	if p.ClientID != "my-client-id" {
		t.Errorf("ClientID = %q, want %q", p.ClientID, "my-client-id")
	}
}

// TestCloseClient_SendsDetachBeforeClose: the recording client must see
// detach queued BEFORE the conn is released — Close discards whatever is
// still in the send queue, so the order is the whole point (D3/3.3).
func TestCloseClient_SendsDetachBeforeClose(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, conn *fakeConn) {
		t.Helper()
		conn.mu.Lock()
		defer conn.mu.Unlock()
		if len(conn.sent) != 1 || conn.sent[0].Type != ipc.MsgDetach {
			t.Errorf("at close time, sent = %+v, want exactly one queued "+
				"MsgDetach — Close discards frames still in the send queue, "+
				"so detach must be queued before Close runs, not after", conn.sent)
		}
	}

	t.Run("single connection", func(t *testing.T) {
		conn := newFakeConn()
		m := Model{client: conn}
		m.SetClientCloser(func(c Client) { check(t, c.(*fakeConn)) })
		m.CloseClient()
	})

	t.Run("router", func(t *testing.T) {
		local, gpu := newFakeConn(), newFakeConn()
		r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
		m := Model{client: r}
		m.SetClientCloser(func(c Client) { check(t, c.(*fakeConn)) })
		m.CloseClient()
	})
}

// TestStatusBar_RoleMarkerOnlyWithTwoClients: the marker is shown only once a
// second client exists, and names the right role.
func TestStatusBar_RoleMarkerOnlyWithTwoClients(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{
		cfg: config.Default(), width: 100, height: 40,
		notifications: NewNotificationCenter(30, 50),
		projects:      []*ProjectModel{{ID: "proj-1", Dest: ""}},
	}
	m.SetClientID("me")

	// One client: no marker, whichever role this client would otherwise hold.
	m.clientCount = map[string]int{"": 1}
	m.sizeMaster = map[string]string{"": "me"}
	if got := m.renderStatusBar(); strings.Contains(got, "[master]") || strings.Contains(got, "[follower]") {
		t.Errorf("status bar shows a role marker with one client: %q", got)
	}

	// Two clients, this one is master.
	m.clientCount[""] = 2
	if got := m.renderStatusBar(); !strings.Contains(got, "[master]") {
		t.Errorf("status bar = %q, want [master]", got)
	}

	// Two clients, this one is a follower.
	m.sizeMaster[""] = "other-client"
	if got := m.renderStatusBar(); !strings.Contains(got, "[follower]") {
		t.Errorf("status bar = %q, want [follower]", got)
	}
}

// TestTakeControlAction_SendsTakeControl covers both front doors: the keymap
// action and the palette command, each sending MsgTakeControl to the active
// destination.
func TestTakeControlAction_SendsTakeControl(t *testing.T) {
	t.Parallel()

	t.Run("key", func(t *testing.T) {
		t.Parallel()
		m := newModelForTest([]string{"T"}, 0)
		m.SetBindings(config.Bindings{
			Preset:    keymap.DefaultPresetName,
			Overrides: map[keymap.ActionID]string{"client.take_control": "ctrl+g"},
		})
		conn := newFakeConn()
		m.client = conn

		_, cmd := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
		runCmd(cmd)

		var saw bool
		for _, msg := range conn.sent {
			if msg.Type == ipc.MsgTakeControl {
				saw = true
			}
		}
		if !saw {
			t.Error("bound key did not send take_control")
		}
	})

	t.Run("palette", func(t *testing.T) {
		t.Parallel()
		m := newModelForTest([]string{"T"}, 0)
		conn := newFakeConn()
		m.client = conn
		m.dialog = dialogCommandPalette

		_, cmd := m.executePaletteCommand(paletteCommand{action: palActTakeControl, enabled: true})
		runCmd(cmd)

		var saw bool
		for _, msg := range conn.sent {
			if msg.Type == ipc.MsgTakeControl {
				saw = true
			}
		}
		if !saw {
			t.Error("palette command did not send take_control")
		}
	})
}
