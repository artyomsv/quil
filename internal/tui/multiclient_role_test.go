package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

	// A second destination, UNTOUCHED by the coming broadcast and with no
	// master of its own reported (isFollower("gpu01") is therefore false —
	// the state a lone-client destination is in). If the fix that removed
	// the extra resizeAllPanes() call from the becoming-master branch ever
	// regresses, resizeAllPanes walks every destination and would resize
	// this one too, even though nothing about it changed.
	otherTab := tabWithPane("gpu-tab", "gpu-pane")
	otherTab.Resize(80, 24)
	m.projects = append(m.projects, &ProjectModel{
		ID: "proj-gpu", Name: "gpu", Dest: "gpu01", tabs: []*TabModel{otherTab},
	})

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

	var frames int
	for _, msg := range conn.sent {
		if msg.Type != ipc.MsgResizePanes {
			continue
		}
		frames++
		if msg.Origin != destLocal {
			t.Errorf("resize_panes frame addressed to origin %q, want %q — "+
				"this broadcast is scoped to dest \"\", and becoming its "+
				"master must not resize an unrelated destination",
				msg.Origin, destLocal)
		}
	}
	if frames != 1 {
		t.Errorf("resize_panes frame count = %d, want exactly 1 — diffResizes' "+
			"own re-send (from the cleared sizedOnce) must be the only "+
			"producer here, not a second resizeAllPanes() batch", frames)
	}
	if !sawResizeFor(t, conn, "pane-1") {
		t.Error("becoming the size master must resize every pane at once, " +
			"even one diffResizes alone would have suppressed")
	}
}

// TestResizeAllPanes_NoRaceAgainstConcurrentMasterChanges is the regression
// test for a real crash, not just a -race finding: resizeAllPanes' returned
// tea.Cmd used to read m.sizeMaster (via isFollower) from INSIDE the
// closure — which Bubble Tea runs on its own goroutine, concurrently with
// whatever Update call comes next — while applyWorkspaceState writes
// m.sizeMaster[dest] = ... IN PLACE on the Update goroutine. Go's runtime
// treats a concurrent map read/write as FATAL, unconditionally: it killed
// this very test with "fatal error: concurrent map read and map write" the
// first time this test was written wrong (calling resizeAllPanes() itself,
// not just its returned closure, from the "Cmd" goroutine — see below).
//
// resizeAllPanes() is called EXACTLY ONCE here, synchronously, before either
// goroutine starts — that single call is what production does too: Update
// calls it on its own one goroutine, so THAT read of m.sizeMaster races
// nothing. What must never race is the RETURNED CLOSURE, which Bubble Tea
// runs on a separate goroutine and which this test then re-invokes many
// times concurrently with further broadcasts — exactly the shape "a Cmd
// still running when the next Update call lands" takes in production.
//
// forUpdate is a SEPARATE Model copy from base, exactly matching Bubble
// Tea's real shape: a Cmd closure holds the Model value from the Update call
// that created it, and the NEXT Update call produces its own separate
// value — no goroutine ever shares a Model STRUCT instance. A plain field
// (m.projects, say) is therefore never raced by this test. sizeMaster is
// different: it is a MAP, a reference type, so copying the struct copies the
// map HEADER only — both copies still point at the same underlying data,
// which is the entire bug.
//
// Run with dev.sh test-race internal/tui. To confirm it catches the OLD
// code: move the `if m.isFollower(proj.Dest) { continue }` line from before
// `return func() tea.Msg {` in resizeAllPanes back inside the closure, and
// re-run — -race fails (and often crashes outright, with no -race needed).
func TestResizeAllPanes_NoRaceAgainstConcurrentMasterChanges(t *testing.T) {
	t.Parallel()
	base, _ := tinyTermModel(t)
	base.SetClientID("me")
	next, cmd0 := base.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd0)
	base = next.(Model)
	// This client is its OWN reported master at the moment resizeAllPanes()
	// is called — a real, pre-existing, non-nil map (so it is genuinely
	// SHARED with forUpdate below, the way a live sizeMaster map always is),
	// and NOT a follower yet, so the pane below is actually included in the
	// batch this call computes. A follower start would make the (correct)
	// outer gate skip the destination entirely, leaving the closure nothing
	// to iterate and the whole test vacuous.
	base.sizeMaster = map[string]string{"": "me"}

	forUpdate := base

	cmd := base.resizeAllPanes()
	if cmd == nil {
		t.Fatal("resizeAllPanes returned nil")
	}

	state := WorkspaceStateMsg{
		Dest: "", SizeMaster: "client-a",
		ActiveProject: "proj-1", ActiveTab: "tab-1",
		Projects: []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:     []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1"}}},
		Panes:    []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}},
	}

	const iterations = 300
	var wg sync.WaitGroup
	wg.Add(2)

	// The Cmd-executor goroutine: re-runs the SAME returned closure many
	// times — a stress-test proxy for "however late this closure actually
	// runs, and however many broadcasts have landed by then, it must still
	// touch nothing shared."
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			cmd()
		}
	}()

	// The Update goroutine: applies broadcasts that flip the size master —
	// applyWorkspaceState's real write path, called directly (as Update
	// does), so this test isolates the sizeMaster race from the unrelated
	// pointer-graph writes resizeTabs makes elsewhere in the WorkspaceStateMsg
	// arm — this test's business is the map, not that graph.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			s := state
			if i%2 == 0 {
				s.SizeMaster = "client-a"
			} else {
				s.SizeMaster = "client-b"
			}
			forUpdate.applyWorkspaceState(s, "")
		}
	}()

	wg.Wait()
}

// TestResizeAllPanes_MixedSession_FollowerOnOneDestMasterOnAnother covers D8:
// a TUI can be the master on one daemon and a follower on another, and each
// destination's resize decision must be independent.
func TestResizeAllPanes_MixedSession_FollowerOnOneDestMasterOnAnother(t *testing.T) {
	t.Parallel()
	localTab := tabWithPane("tab-local", "pane-local")
	localTab.Resize(80, 24)
	gpuTab := tabWithPane("tab-gpu", "pane-gpu")
	gpuTab.Resize(80, 24)

	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})

	m := Model{
		client: r, sized: true, width: 172, height: 48,
		tabDragFromIdx: -1,
		projects: []*ProjectModel{
			{ID: "proj-local", Name: "Local", Dest: "", tabs: []*TabModel{localTab}},
			{ID: "proj-gpu", Name: "GPU", Dest: "gpu01", tabs: []*TabModel{gpuTab}},
		},
	}
	m.SetClientID("me")
	// No master reported locally (this client acts as master by default);
	// someone else is master on gpu01.
	m.sizeMaster = map[string]string{"gpu01": "other-client"}

	cmd := m.resizeAllPanes()
	if cmd == nil {
		t.Fatal("resizeAllPanes returned nil")
	}
	runCmd(cmd)

	if !sawResizeFor(t, local, "pane-local") {
		t.Error("the destination this client masters must be resized")
	}
	if sawResizePanes(gpu) {
		t.Error("the destination this client FOLLOWS must not be resized")
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

// TestClientGeometryCmd_ReportsZeroWhenUnpaintable mirrors
// TestAttachMessage_ReportsNoGeometryBelowTheMinimum: a terminal too small to
// paint must report 0x0, never the raw sub-floor size. The daemon uses this
// exact rule for master eligibility (§3.3: "the raw values are used, never
// the 80x24 default... a console-less client attaches at 0x0 and must never
// be elected") — client_geometry has to honour the same floor after attach,
// or a window that shrank below it would still look eligible.
func TestClientGeometryCmd_ReportsZeroWhenUnpaintable(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		width, height int
		wantZero      bool
	}{
		{"console-less client", 1, 1, true},
		{"one column short", minTermWidth - 1, 40, true},
		{"one row short", 120, minTermHeight - 1, true},
		{"exactly the minimum", minTermWidth, minTermHeight, false},
		{"ordinary terminal", 172, 48, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn := newFakeConn()
			m := Model{client: conn, width: tt.width, height: tt.height}
			cmd := m.clientGeometryCmd()
			if cmd == nil {
				t.Fatal("clientGeometryCmd returned nil")
			}
			runCmd(cmd)

			var p ipc.ClientGeometryPayload
			var found bool
			for _, msg := range conn.sent {
				if msg.Type == ipc.MsgClientGeometry {
					if err := msg.DecodePayload(&p); err != nil {
						t.Fatalf("decode client_geometry: %v", err)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("no client_geometry sent")
			}
			if tt.wantZero {
				if p.Cols != 0 || p.Rows != 0 {
					t.Errorf("geometry = %dx%d at %dx%d, want 0x0",
						p.Cols, p.Rows, tt.width, tt.height)
				}
				return
			}
			if p.Cols != tt.width || p.Rows != tt.height {
				t.Errorf("geometry = %dx%d, want %dx%d (the raw window size)",
					p.Cols, p.Rows, tt.width, tt.height)
			}
		})
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
		var closed int
		m := Model{client: conn}
		m.SetClientCloser(func(c Client) {
			closed++
			check(t, c.(*fakeConn))
		})
		m.CloseClient()
		// The closer running is what makes check() mean anything — a mutation
		// that dropped the closeClient(c) call entirely would leave check()
		// never invoked, and this test would pass having asserted nothing.
		if closed != 1 {
			t.Errorf("closer ran %d times, want 1", closed)
		}
	})

	t.Run("router", func(t *testing.T) {
		local, gpu := newFakeConn(), newFakeConn()
		r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
		var closed int
		m := Model{client: r}
		m.SetClientCloser(func(c Client) {
			closed++
			check(t, c.(*fakeConn))
		})
		m.CloseClient()
		if closed != 2 {
			t.Errorf("closer ran %d times, want 2 (one per conn)", closed)
		}
	})
}

// TestCloseClient_DoesNotHangOnAWedgedConn covers the fix for a real
// usability bug: ipc.Client.Send can block up to clientSendTimeout (5s)
// against a peer whose must-deliver queue never drains, and CloseClient used
// to detach each conn SEQUENTIALLY — so a handful of dead remote hosts could
// turn Ctrl+Q into a many-second hang. detachTimeout bounds the wait; a conn
// whose Send never returns must still be closed once the budget expires.
func TestCloseClient_DoesNotHangOnAWedgedConn(t *testing.T) {
	old := detachTimeout
	detachTimeout = 20 * time.Millisecond
	t.Cleanup(func() { detachTimeout = old })

	wedged := &blockingSendConn{block: make(chan struct{})} // never closed
	t.Cleanup(func() { close(wedged.block) })               // let the goroutine finish, don't leak it

	var closed int
	m := Model{client: wedged}
	m.SetClientCloser(func(Client) { closed++ })

	done := make(chan struct{})
	go func() {
		m.CloseClient()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseClient hung on a wedged conn's detach send")
	}
	if closed != 1 {
		t.Errorf("closer ran %d times, want 1 — a wedged detach must not stop the conn from being closed", closed)
	}
}

// blockingSendConn's Send never returns until block is closed, simulating a
// peer whose must-deliver queue is permanently full.
type blockingSendConn struct{ block chan struct{} }

func (b *blockingSendConn) Send(*ipc.Message) error {
	<-b.block
	return nil
}
func (b *blockingSendConn) Receive() (*ipc.Message, error) { return nil, nil }

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
