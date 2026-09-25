package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Size authority: one PTY has one size, so only the size master may set it,
// and every follower learns a new size BEFORE the child repaints at it. Every
// test here drives real conns through ipc.Server, because the gate reads which
// CONN sent the resize and a direct handler call has no conn to read.

// resizeProbeSession is a PTY that records its Resize calls and can run a hook
// inside one, the way a real child answers SIGWINCH with a repaint.
type resizeProbeSession struct {
	fakeSession
	mu       sync.Mutex
	calls    [][2]uint16 // (rows, cols)
	onResize func()
	fail     bool
}

func (s *resizeProbeSession) Resize(rows, cols uint16) error {
	s.mu.Lock()
	s.calls = append(s.calls, [2]uint16{rows, cols})
	hook, fail := s.onResize, s.fail
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return errors.New("simulated resize failure")
	}
	return nil
}

// Write accepts the redraw key repaintAfterResize enqueues.
func (s *resizeProbeSession) Write(b []byte) (int, error) { return len(b), nil }

func (s *resizeProbeSession) resizeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// resizeAuthorityDaemon is a daemon behind a real IPC server with a fake
// spawn path, fake grace timers, and one empty tab. The tab keeps attach from
// creating the default workspace, whose shell is a real PTY.
func resizeAuthorityDaemon(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	// Fake timers, so no real grace timer outlives the test when the conns
	// close. The fake clock is fixed, so attach order ties are broken by id:
	// "A" is the older client.
	(&clientsHarness{t: t}).install(d, testGrace)
	tab := d.session.CreateTab("T")
	return d, sock, tab.ID
}

// attachAB attaches A at 200x50 and then B at 100x30. It waits for A's
// election before B dials, because two conns' attaches race and a connected
// master keeps its slot: B winning the race would stay the master.
func attachAB(t *testing.T, d *Daemon, sock string) (a, b *ipc.Client) {
	t.Helper()
	a = attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A elected", func() bool { return d.masterID() == "A" })
	b = attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	return a, b
}

// addProbePanes adds n panes of type typ to the tab, each with a probe PTY.
// Added AFTER the clients attach, so attach's redraw kick resizes none.
func addProbePanes(t *testing.T, d *Daemon, tabID, typ string, n int) ([]*Pane, []*resizeProbeSession) {
	t.Helper()
	panes := make([]*Pane, n)
	probes := make([]*resizeProbeSession, n)
	for i := range panes {
		p, err := d.session.CreatePane(tabID, "")
		if err != nil {
			t.Fatalf("create pane: %v", err)
		}
		s := &resizeProbeSession{}
		p.PluginMu.Lock()
		p.Type = typ
		p.PTY = s
		p.PluginMu.Unlock()
		t.Cleanup(p.StopInput)
		panes[i], probes[i] = p, s
	}
	return panes, probes
}

func appliedSize(p *Pane) (int, int) {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.appliedCols, p.appliedRows
}

// loadRedrawKeyPlugin registers a claude-like plugin: one that declares a
// redraw_key, so a resize makes repaintAfterResize send it input.
func loadRedrawKeyPlugin(t *testing.T, d *Daemon, name string) {
	t.Helper()
	dir := t.TempDir()
	toml := "[plugin]\n" +
		"name = \"" + name + "\"\n" +
		"display_name = \"" + name + "\"\n" +
		"category = \"test\"\n" +
		"schema_version = 1\n" +
		"[command]\n" +
		"cmd = \"echo\"\n" +
		"[persistence]\n" +
		"ghost_buffer = false\n" +
		"redraw_key = \"\\f\"\n"
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write plugin toml: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if p := d.registry.Get(name); p == nil || p.Persistence.RedrawKey == "" {
		t.Fatalf("plugin %q not loaded with a redraw_key", name)
	}
}

// readUntil reads c's frames until one matches, returning every frame read,
// the match last. It fails the test when none matches in time.
func readUntil(t *testing.T, c *ipc.Client, what string, match func(*ipc.Message) bool) []*ipc.Message {
	t.Helper()
	var got []*ipc.Message
	if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	for {
		m, err := c.Receive()
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		got = append(got, m)
		if match(m) {
			return got
		}
	}
}

// readFor reads every frame c receives within d. It is the last read on c:
// a deadline that expires mid-frame leaves the stream unusable.
func readFor(c *ipc.Client, d time.Duration) []*ipc.Message {
	var got []*ipc.Message
	if err := c.SetReadDeadline(time.Now().Add(d)); err != nil {
		return nil
	}
	for {
		m, err := c.Receive()
		if err != nil {
			return got
		}
		got = append(got, m)
	}
}

func isType(typ string) func(*ipc.Message) bool {
	return func(m *ipc.Message) bool { return m.Type == typ }
}

func paneSizesOf(t *testing.T, m *ipc.Message) []ipc.ResizePanePayload {
	t.Helper()
	var p ipc.PaneSizesPayload
	if err := m.DecodePayload(&p); err != nil {
		t.Fatalf("decode pane_sizes: %v", err)
	}
	return p.Panes
}

func countType(msgs []*ipc.Message, typ string) int {
	n := 0
	for _, m := range msgs {
		if m.Type == typ {
			n++
		}
	}
	return n
}

// barrier returns once every frame c sent before it has been dispatched. A
// conn's frames run in order, so a pane_input stamping the client's input time
// is a fence for the resizes queued ahead of it.
// The registry clock is moved to a fresh instant first, so the stamp this
// barrier waits for cannot be one an earlier message left.
func barrier(t *testing.T, d *Daemon, c *ipc.Client, id string) {
	t.Helper()
	d.clients.mu.Lock()
	stamp := d.clients.clock().Add(time.Minute)
	d.clients.now = func() time.Time { return stamp }
	d.clients.mu.Unlock()
	sendClientMsg(t, c, ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-none", Data: []byte("x")})
	waitUntil(t, id+"'s barrier", func() bool {
		rec, _ := clientRecordByID(d, id)
		return rec.lastInputAt.Equal(stamp)
	})
}

// Spec 9.1.1: two clients, and only the master's resize reaches the PTY.
func TestResizeAuthority_OnlyMasterResizes(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	a, b := attachAB(t, d, sock)
	if d.masterID() != "A" {
		t.Fatalf("masterID = %q, want A", d.masterID())
	}
	panes, probes := addProbePanes(t, d, tabID, "terminal", 1)

	sendClientMsg(t, b, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: panes[0].ID, Cols: 90, Rows: 20})
	barrier(t, d, b, "B")
	if c, r := appliedSize(panes[0]); c != 0 || r != 0 || probes[0].resizeCount() != 0 {
		t.Fatalf("a follower's resize applied: applied=%dx%d, Resize calls=%d", c, r, probes[0].resizeCount())
	}

	sendClientMsg(t, a, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: panes[0].ID, Cols: 150, Rows: 40})
	waitUntil(t, "the master's resize applied", func() bool {
		c, r := appliedSize(panes[0])
		return c == 150 && r == 40
	})
}

// With no master and no reserved slot, any attached client's resize applies:
// the legacy single-client behaviour, which keeps an older client working.
func TestResizeAuthority_OpenWhenNoMaster(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	c := attachClientAs(t, sock, "headless", 0, 0)
	waitUntil(t, "attached", func() bool { return d.clientCount() == 1 })
	if d.masterID() != "" || !d.sizeAuthorityOpen() {
		t.Fatalf("masterID = %q, open = %v; want no master and open authority", d.masterID(), d.sizeAuthorityOpen())
	}
	panes, _ := addProbePanes(t, d, tabID, "terminal", 1)

	sendClientMsg(t, c, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: panes[0].ID, Cols: 100, Rows: 40})
	waitUntil(t, "the resize applied", func() bool {
		cols, rows := appliedSize(panes[0])
		return cols == 100 && rows == 40
	})
}

// Review Focus 2: a resize burst across 48 panes must reach a follower as ONE
// frame. One frame per pane is 48 must-deliver frames on a 64-slot queue, on
// top of whatever else that follower is owed.
func TestResizePanes_BatchSendsOnePaneSizesFramePerFollower(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	a, b := attachAB(t, d, sock)
	// B's own attach frames first, so the count below sees only the burst.
	readUntil(t, b, "B's attach state", isType(ipc.MsgWorkspaceState))

	const n = 48
	panes, probes := addProbePanes(t, d, tabID, "terminal", n)
	batch := make([]ipc.ResizePanePayload, n)
	for i, p := range panes {
		batch[i] = ipc.ResizePanePayload{PaneID: p.ID, Cols: 120, Rows: 40}
	}
	sendClientMsg(t, a, ipc.MsgResizePanes, ipc.ResizePanesPayload{Panes: batch})
	waitUntil(t, "every pane resized", func() bool {
		for _, s := range probes {
			if s.resizeCount() != 1 {
				return false
			}
		}
		return true
	})

	got := readFor(b, 300*time.Millisecond)
	var frames []*ipc.Message
	for _, m := range got {
		if m.Type == ipc.MsgPaneSizes {
			frames = append(frames, m)
		}
	}
	if len(frames) != 1 {
		t.Fatalf("B received %d pane_sizes frames, want exactly 1", len(frames))
	}
	if entries := paneSizesOf(t, frames[0]); len(entries) != n {
		t.Errorf("the pane_sizes frame has %d entries, want %d", len(entries), n)
	}
	if d.clientCount() != 2 {
		t.Errorf("clientCount = %d after the burst, want 2: B was disconnected", d.clientCount())
	}
}

// A batch naming one pane twice resizes it once, to the last size, and tells
// the follower that size alone.
func TestResizePanes_DuplicatePaneResizedOnceToLastSize(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	a, b := attachAB(t, d, sock)
	readUntil(t, b, "B's attach state", isType(ipc.MsgWorkspaceState))

	panes, probes := addProbePanes(t, d, tabID, "terminal", 1)
	id := panes[0].ID
	sendClientMsg(t, a, ipc.MsgResizePanes, ipc.ResizePanesPayload{Panes: []ipc.ResizePanePayload{
		{PaneID: id, Cols: 100, Rows: 30},
		{PaneID: id, Cols: 110, Rows: 35},
	}})
	got := readUntil(t, b, "the size frame", isType(ipc.MsgPaneSizes))
	if e := paneSizesOf(t, got[len(got)-1]); len(e) != 1 || e[0].Cols != 110 || e[0].Rows != 35 {
		t.Errorf("pane_sizes = %+v, want one entry at 110x35", e)
	}
	waitUntil(t, "the resize applied", func() bool {
		c, r := appliedSize(panes[0])
		return c == 110 && r == 35
	})
	if n := probes[0].resizeCount(); n != 1 {
		t.Errorf("Resize called %d times, want 1", n)
	}
}

// Spec 5d: the follower must hold the new size before the child's repaint
// arrives, or the repaint lands in the old-sized VT and is reflowed. The probe
// repaints INSIDE Resize, then pauses, so a size frame sent after Resize would
// reach B behind the repaint.
func TestPaneSizes_ArriveBeforeRepaintOutput(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	loadRedrawKeyPlugin(t, d, "claude-like")
	a, b := attachAB(t, d, sock)
	readUntil(t, b, "B's attach state", isType(ipc.MsgWorkspaceState))

	panes, probes := addProbePanes(t, d, tabID, "claude-like", 1)
	pane := panes[0]
	probes[0].mu.Lock()
	probes[0].onResize = func() {
		d.flushPaneOutput(pane.ID, []byte("REPAINT"))
		time.Sleep(50 * time.Millisecond)
	}
	probes[0].mu.Unlock()

	sendClientMsg(t, a, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: pane.ID, Cols: 150, Rows: 40})
	got := readUntil(t, b, "the REPAINT output", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgPaneOutput {
			return false
		}
		var p ipc.PaneOutputPayload
		return m.DecodePayload(&p) == nil && p.PaneID == pane.ID && string(p.Data) == "REPAINT"
	})
	sawSize := false
	for _, m := range got {
		if m.Type != ipc.MsgPaneSizes {
			continue
		}
		for _, e := range paneSizesOf(t, m) {
			if e.PaneID == pane.ID && e.Cols == 150 && e.Rows == 40 {
				sawSize = true
			}
		}
	}
	if !sawSize {
		t.Fatal("B received the repaint before the pane_sizes frame carrying 150x40")
	}
}

// A refused resize (a follower's, or a degenerate one) and a same-size one
// change nothing, so they must send no size frame. The last step proves the
// read would have seen one.
func TestPaneSizes_NoneForRefusedOrSameSize(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	a, b := attachAB(t, d, sock)
	readUntil(t, b, "B's attach state", isType(ipc.MsgWorkspaceState))

	panes, _ := addProbePanes(t, d, tabID, "terminal", 1)
	pane := panes[0]
	pane.PluginMu.Lock()
	pane.appliedCols, pane.appliedRows = 120, 40
	pane.PluginMu.Unlock()

	sendClientMsg(t, b, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: pane.ID, Cols: 90, Rows: 20})  // follower
	sendClientMsg(t, a, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: pane.ID, Cols: 120, Rows: 40}) // same size
	sendClientMsg(t, a, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: pane.ID, Cols: 1, Rows: 1})    // degenerate
	barrier(t, d, b, "B")
	barrier(t, d, a, "A")
	sendClientMsg(t, a, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: pane.ID, Cols: 130, Rows: 40}) // applies

	got := readUntil(t, b, "the applied resize's size frame", isType(ipc.MsgPaneSizes))
	entries := paneSizesOf(t, got[len(got)-1])
	if len(entries) != 1 || entries[0].Cols != 130 || entries[0].Rows != 40 {
		t.Errorf("first pane_sizes on B = %+v, want only the applied 130x40", entries)
	}
}

// A resize the PTY refused leaves the child at its previous size, and the
// follower was already told the new one. A second frame puts it back.
func TestPaneSizes_FailedResizeSendsPreviousSize(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	a, b := attachAB(t, d, sock)
	readUntil(t, b, "B's attach state", isType(ipc.MsgWorkspaceState))

	panes, probes := addProbePanes(t, d, tabID, "terminal", 1)
	pane := panes[0]
	pane.PluginMu.Lock()
	pane.appliedCols, pane.appliedRows = 120, 40
	pane.PluginMu.Unlock()
	probes[0].mu.Lock()
	probes[0].fail = true
	probes[0].mu.Unlock()

	sendClientMsg(t, a, ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: pane.ID, Cols: 150, Rows: 50})
	first := readUntil(t, b, "the new size", isType(ipc.MsgPaneSizes))
	if e := paneSizesOf(t, first[len(first)-1]); len(e) != 1 || e[0].Cols != 150 || e[0].Rows != 50 {
		t.Fatalf("first pane_sizes = %+v, want 150x50", e)
	}
	second := readUntil(t, b, "the rollback", isType(ipc.MsgPaneSizes))
	if e := paneSizesOf(t, second[len(second)-1]); len(e) != 1 || e[0].Cols != 120 || e[0].Rows != 40 {
		t.Fatalf("second pane_sizes = %+v, want the previous 120x40", e)
	}
	if c, r := appliedSize(pane); c != 120 || r != 40 {
		t.Errorf("applied = %dx%d after a failed resize, want 120x40 unchanged", c, r)
	}
}

// The broadcast carries who the master is and how many clients are attached;
// the map written to workspace.json carries neither.
func TestWorkspaceState_CarriesSizeMasterAndClients(t *testing.T) {
	d, sock, _ := resizeAuthorityDaemon(t)
	attachAB(t, d, sock)

	state := d.buildWorkspaceState()
	if got, _ := state["size_master"].(string); got != "A" {
		t.Errorf("size_master = %v, want A", state["size_master"])
	}
	if got, _ := state["clients"].(int); got != 2 {
		t.Errorf("clients = %v, want 2", state["clients"])
	}

	activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
	for _, overlays := range []bool{false, true} {
		m := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, overlays)
		for _, k := range []string{"size_master", "clients"} {
			if _, ok := m[k]; ok {
				t.Errorf("workspaceStateFromSnapshot(includeOverlays=%v) has %q", overlays, k)
			}
		}
	}
}

// A master change reaches every client as a broadcast carrying the new
// size_master. A same-id reattach inside the grace changes nothing, so it
// costs the follower no frame.
func TestMasterChange_IsBroadcastAndReattachIsNot(t *testing.T) {
	d, sock, _ := resizeAuthorityDaemon(t)
	a, b := attachAB(t, d, sock)
	readUntil(t, b, "B's attach state", isType(ipc.MsgWorkspaceState))

	sendClientMsg(t, b, ipc.MsgTakeControl, nil)
	readUntil(t, b, "a state naming B the master", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgWorkspaceState {
			return false
		}
		var s map[string]any
		return m.DecodePayload(&s) == nil && s["size_master"] == "B"
	})

	// Hand the slot back, then lose A's link: its slot is reserved.
	sendClientMsg(t, a, ipc.MsgTakeControl, nil)
	readUntil(t, b, "a state naming A the master", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgWorkspaceState {
			return false
		}
		var s map[string]any
		return m.DecodePayload(&s) == nil && s["size_master"] == "A"
	})
	a.Close()
	waitUntil(t, "A's link lost", func() bool { return d.clientCount() == 1 })
	if d.masterID() != "A" || d.sizeAuthorityOpen() {
		t.Fatalf("masterID = %q, open = %v; want A's slot reserved", d.masterID(), d.sizeAuthorityOpen())
	}

	a2 := attachClientAs(t, sock, "A", 200, 50)
	readUntil(t, a2, "A's own attach state", isType(ipc.MsgWorkspaceState))
	if n := countType(readFor(b, 300*time.Millisecond), ipc.MsgWorkspaceState); n != 0 {
		t.Errorf("B received %d workspace_state frames for a same-id reattach, want 0", n)
	}
	if d.masterID() != "A" {
		t.Errorf("masterID = %q after the reattach, want A", d.masterID())
	}
}

// clientSize used to be whichever client attached LAST. A new pane now starts
// at the master's window, whatever order the clients came in.
func TestInitialPaneSize_UsesMasterGeometry(t *testing.T) {
	d, sock, tabID := resizeAuthorityDaemon(t)
	_, b := attachAB(t, d, sock)
	if d.masterID() != "A" {
		t.Fatalf("masterID = %q, want A", d.masterID())
	}

	sendClientMsg(t, b, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tabID})
	waitUntil(t, "the pane created", func() bool { return len(d.session.Panes(tabID)) == 1 })
	pane := d.session.Panes(tabID)[0]
	if c, r := paneSize(pane); c != 200 || r != 50 {
		t.Errorf("new pane size = %dx%d, want the master's 200x50", c, r)
	}
}

// A self-reported window size is bounded before it feeds a new pane's size or
// the election: a value above the ceiling is taken as the ceiling.
func TestClientGeometry_ClampedToMax(t *testing.T) {
	d, sock, _ := resizeAuthorityDaemon(t)
	c := attachClientAs(t, sock, "A", 5000, 4000)
	waitUntil(t, "attached", func() bool { return d.clientCount() == 1 })
	if rec, _ := clientRecordByID(d, "A"); rec.cols != maxClientDim || rec.rows != maxClientDim {
		t.Errorf("attach geometry = %dx%d, want %dx%d", rec.cols, rec.rows, maxClientDim, maxClientDim)
	}
	if sz := d.clientSize.Load(); sz == nil || sz.cols != maxClientDim || sz.rows != maxClientDim {
		t.Errorf("clientSize = %+v, want %dx%d", sz, maxClientDim, maxClientDim)
	}

	sendClientMsg(t, c, ipc.MsgClientGeometry, ipc.ClientGeometryPayload{Cols: 3000, Rows: 70})
	waitUntil(t, "geometry recorded", func() bool {
		rec, _ := clientRecordByID(d, "A")
		return rec.rows == 70
	})
	if rec, _ := clientRecordByID(d, "A"); rec.cols != maxClientDim {
		t.Errorf("client_geometry cols = %d, want %d", rec.cols, maxClientDim)
	}
}

// Stop must disarm the grace timer, so it never fires into a stopped daemon.
func TestStop_DisarmsTheGraceTimer(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	h := &clientsHarness{t: t}
	h.install(d, testGrace)
	a, _ := h.attach("A", 200, 50)
	h.attach("B", 100, 30)
	d.forgetAttachedClient(a)
	if h.armed() == nil {
		t.Fatal("setup: losing the master armed no grace timer")
	}
	d.Stop()
	if h.armed() != nil {
		t.Error("the grace timer is still armed after Stop")
	}
}
