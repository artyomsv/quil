package daemon

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

func attachOn(t *testing.T, c *ipc.Client, id string) {
	t.Helper()
	sendNoID(t, c, ipc.MsgAttach, ipc.AttachPayload{Cols: 120, Rows: 40, WinCols: 120, WinRows: 40, ClientID: id, CWD: t.TempDir()})
}

func waitType(t *testing.T, c *ipc.Client, typ string, within time.Duration) *ipc.Message {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(within))
	defer c.SetReadDeadline(time.Time{})
	for {
		m, err := c.Receive()
		if err != nil {
			t.Fatalf("no %s: %v", typ, err)
		}
		if m.Type == typ {
			return m
		}
	}
}

// A read-only attach on an empty daemon watches: it bootstraps no tab, sets
// no default pane size, is never size master, still receives live output,
// and is still refused anything that acts.
func TestReadOnly_AttachOnEmptyDaemon(t *testing.T) {
	h := newAuthHarness(t)
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, ro, "viewer-1")
	waitType(t, ro, ipc.MsgWorkspaceState, 3*time.Second)
	if n := len(h.d.session.Tabs()); n != 0 {
		t.Fatalf("a read-only attach bootstrapped %d tab(s)", n)
	}
	if h.d.clientSize.Load() != nil {
		t.Fatal("a read-only attach set the default pane size")
	}
	if h.d.masterID() != "" {
		t.Fatalf("a read-only client became size master (%q)", h.d.masterID())
	}
	out, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "p", Data: []byte("hi")})
	h.d.broadcast(out)
	waitType(t, ro, ipc.MsgPaneOutput, 3*time.Second)
	if !sendAndProbe(t, ro, &ipc.Message{Type: ipc.MsgSwitchTab, ID: "ro-switch", Payload: []byte(`{}`)}) {
		t.Fatal("switch_tab from a read-only conn was not refused")
	}
}

// A viewer's directory is a path on ITS machine: recorded, it would become a
// defaultCWD candidate and open the owner's next pane there.
func TestReadOnly_AttachDropsCWD(t *testing.T) {
	h := newAuthHarness(t)
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, ro, "viewer-1")
	waitType(t, ro, ipc.MsgWorkspaceState, 3*time.Second)
	rec, ok := h.d.clientByConn(viewerConn(t, h, "viewer"))
	if !ok {
		t.Fatal("the read-only attach left no client record")
	}
	if rec.cwd != "" {
		t.Fatalf("a read-only attach recorded cwd %q", rec.cwd)
	}
	// Control: a full token's attach records its directory.
	full, _ := h.login(t, h.mint(t, "owner", clientauth.LevelFull, nil))
	attachOn(t, full, "owner-1")
	waitType(t, full, ipc.MsgWorkspaceState, 3*time.Second)
	if rec, _ := h.d.clientByConn(viewerConn(t, h, "owner")); rec.cwd == "" {
		t.Fatal("control: a full attach recorded no cwd — the test cannot fail")
	}
	finishDispatch(t, full) // the full attach bootstraps a tab past its state frame
}

// deferredPane builds a restored-but-not-spawned pane and counts spawns
// through newSessionFn. restored is its restored OutputBuf content; "" builds
// a pane with NO replay buffer at all.
func deferredPane(t *testing.T, h *authHarness, restored string) (*Pane, *atomic.Int32) {
	t.Helper()
	var spawns atomic.Int32
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { spawns.Add(1); return newLiveFakeSession() }
	t.Cleanup(func() { newSessionFn = prev })
	tab := h.d.session.CreateTab("deferred")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.PluginMu.Lock()
	pane.Type = "terminal"
	pane.PluginMu.Unlock()
	pane.spawnMu.Lock()
	pane.Pending = true
	pane.spawnMu.Unlock()
	if restored != "" {
		pane.OutputBuf.Write([]byte(restored))
	}
	return pane, &spawns
}

func TestReadOnly_ReadOutputSpawnsNothing(t *testing.T) {
	h := newAuthHarness(t)
	pane, spawns := deferredPane(t, h, "restored line\n")
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	resp := decodeInto[ipc.ReadPaneOutputRespPayload](t, roundTrip(t, ro, ipc.MsgReadPaneOutputReq,
		ipc.MsgReadPaneOutputResp, ipc.ReadPaneOutputReqPayload{PaneID: pane.ID}))
	if spawns.Load() != 0 {
		t.Fatal("a read-only read spawned the deferred pane")
	}
	if !resp.NotRunning || resp.Text != "restored line" {
		t.Fatalf("resp = %+v, want the restored buffer and not_running", resp)
	}
	// Positive control: a full conn's read does spawn it.
	full, _ := h.login(t, h.mint(t, "owner", clientauth.LevelFull, nil))
	roundTrip(t, full, ipc.MsgReadPaneOutputReq, ipc.MsgReadPaneOutputResp, ipc.ReadPaneOutputReqPayload{PaneID: pane.ID})
	if spawns.Load() != 1 {
		t.Fatalf("control: full read spawned %d, want 1", spawns.Load())
	}
}

func TestReadOnly_ScreenshotSpawnsNothing(t *testing.T) {
	h := newAuthHarness(t)
	pane, spawns := deferredPane(t, h, "restored line\n")
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	resp := decodeInto[ipc.ScreenshotPaneRespPayload](t, roundTrip(t, ro, ipc.MsgScreenshotPaneReq,
		ipc.MsgScreenshotPaneResp, ipc.ScreenshotPaneReqPayload{PaneID: pane.ID}))
	if spawns.Load() != 0 || !resp.NotRunning {
		t.Fatalf("spawns=%d resp=%+v", spawns.Load(), resp)
	}
}

// A deferred pane with NO replay buffer answers empty with not_running, for
// both reads, and is not spawned to fill the gap.
func TestReadOnly_NoBufferSpawnsNothing(t *testing.T) {
	h := newAuthHarness(t)
	pane, spawns := deferredPane(t, h, "")
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	out := decodeInto[ipc.ReadPaneOutputRespPayload](t, roundTrip(t, ro, ipc.MsgReadPaneOutputReq,
		ipc.MsgReadPaneOutputResp, ipc.ReadPaneOutputReqPayload{PaneID: pane.ID}))
	shot := decodeInto[ipc.ScreenshotPaneRespPayload](t, roundTrip(t, ro, ipc.MsgScreenshotPaneReq,
		ipc.MsgScreenshotPaneResp, ipc.ScreenshotPaneReqPayload{PaneID: pane.ID}))
	if spawns.Load() != 0 {
		t.Fatalf("read-only reads of an empty deferred pane spawned it %d time(s)", spawns.Load())
	}
	if !out.NotRunning || out.Text != "" || !shot.NotRunning || shot.Text != "" {
		t.Fatalf("read=%+v screenshot=%+v, want empty answers with not_running", out, shot)
	}
}

// A live pane with NO replay buffer is exactly what makes attach call
// redrawKick; a read-only attach must not, a full attach (control) must.
func TestReadOnly_AttachNoKick(t *testing.T) {
	h := newAuthHarness(t)
	_, sess := livePane(t, h)
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, ro, "viewer-1")
	roundTrip(t, ro, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{}) // attach has fully run
	if w, r := sess.counts(); w != 0 || r != 0 {
		t.Fatalf("read-only attach wrote %d / resized %d times", w, r)
	}
	full, _ := h.login(t, h.mint(t, "owner", clientauth.LevelFull, nil))
	attachOn(t, full, "owner-1")
	roundTrip(t, full, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
	if w, r := sess.counts(); w == 0 && r == 0 {
		t.Fatal("control: a full attach did not kick the pane — the test cannot fail")
	}
}

func TestReadOnly_AttachKeepsGhostSnap(t *testing.T) {
	h := newAuthHarness(t)
	pane, spawns := deferredPane(t, h, "restored line\n")
	pane.PluginMu.Lock()
	pane.GhostSnap = []byte("previous session\n")
	pane.PluginMu.Unlock()
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, ro, "viewer-1")
	roundTrip(t, ro, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
	pane.PluginMu.Lock()
	kept := pane.GhostSnap != nil
	pane.PluginMu.Unlock()
	if !kept {
		t.Fatal("a viewer's attach consumed the owner's first-attach replay")
	}
	// GUARD (ruling P-7): handleAttach never spawns today; a read-only attach
	// on a deferred pane must keep it that way.
	if n := spawns.Load(); n != 0 {
		t.Fatalf("a read-only attach spawned the deferred pane %d time(s)", n)
	}
}

// viewerConn is the server-side conn of the logged-in token named name.
func viewerConn(t *testing.T, h *authHarness, name string) *ipc.Conn {
	t.Helper()
	var found *ipc.Conn
	waitUntil(t, "the server conn of "+name, func() bool {
		for _, c := range h.d.server.ConnsSnapshot() {
			if a := c.Auth(); a != nil && a.TokenName == name {
				found = c
				return true
			}
		}
		return false
	})
	return found
}

// A read-only attach injects no redraw key and sends no resize, and that
// includes the output hold's overflow path, which asks each "lost" pane to
// repaint through redrawKickPane — a redraw key on the PTY, or a resize
// jiggle. For a read-only conn's hold it must do neither.
// TestHold_OverflowDropsPaneAndKicks (outputhold_test.go) is the control: a
// local conn's hold does kick.
func TestReadOnly_HoldOverflowNoKick(t *testing.T) {
	h := newAuthHarness(t)
	loadRedrawKeyPlugin(t, h.d, "kicky")
	tab := h.d.session.CreateTab("T")
	p, err := h.d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	probe := &resizeProbeSession{}
	p.PluginMu.Lock()
	p.Type, p.PTY = "kicky", probe
	p.PluginMu.Unlock()
	t.Cleanup(p.StopInput)
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	conn := viewerConn(t, h, "viewer")

	h.d.beginOutputHold(conn)
	big := bytes.Repeat([]byte{'p'}, 64<<10)
	for sent := 0; sent <= outputHoldLimit; sent += len(big) {
		h.d.flushPaneOutput(p.ID, big)
	}
	h.d.releaseOutputHold(conn, nil)
	roundTrip(t, ro, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{}) // the conn still works
	time.Sleep(50 * time.Millisecond)                                     // an async kick would land here
	if n := p.inputEnqueued.Load(); n != 0 {
		t.Fatalf("a read-only conn's hold overflow wrote a redraw key %d time(s)", n)
	}
	if n := probe.resizeCount(); n != 0 {
		t.Fatalf("a read-only conn's hold overflow resized the PTY %d time(s)", n)
	}
}

// A viewer that read the owner's client id (list_clients shows ids) cannot
// claim it: the attach is refused, the owner keeps its record and master
// role, and the refusal is audited.
func TestClientID_ViewerCannotClaimOwnersID(t *testing.T) {
	h := newAuthHarness(t)
	owner := attachTestClientID(t, h.sock, "owner-1")
	defer owner.Close()
	waitUntil(t, "owner master", func() bool { return h.d.masterID() == "owner-1" })
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	attachOn(t, ro, "owner-1")
	if p := waitErrorFrame(t, ro, 3*time.Second); p.Code != ipc.ErrCodeRefused || p.Message != "client id in use" {
		t.Fatalf("got %+v, want refused: client id in use", p)
	}
	if h.d.masterID() != "owner-1" || h.d.clientCount() != 1 {
		t.Fatalf("master=%q clients=%d after the impersonation attempt", h.d.masterID(), h.d.clientCount())
	}
	h.waitAudit(t, "refused attach", func(e auditEntry) bool {
		return e.Event == "refused" && e.Type == ipc.MsgAttach && e.Reason == "client id in use"
	})
	// Every retry is refused, but the audit line is written at most once per
	// (conn, type) per minute.
	for i := 0; i < 2; i++ {
		attachOn(t, ro, "owner-1")
		if p := waitErrorFrame(t, ro, 3*time.Second); p.Message != "client id in use" {
			t.Fatalf("retry %d got %+v", i, p)
		}
	}
	n := 0
	for _, e := range h.auditEntries(t) {
		if e.Event == "refused" && e.Type == ipc.MsgAttach {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d refused-attach audit lines for 3 attempts in a minute, want 1", n)
	}
	finishDispatch(t, owner) // the owner's attach bootstraps a tab past the election
}

// GUARD (ruling P-7): a local reconnect replacing its own dead record passes
// on today's tree; it pins that the principal check leaves that path alone.
func TestClientID_LocalReconnectReplaces(t *testing.T) {
	h := newAuthHarness(t)
	first := attachTestClientID(t, h.sock, "owner-1")
	defer first.Close()
	waitUntil(t, "first attached", func() bool { return h.d.clientCount() == 1 })
	second := attachTestClientID(t, h.sock, "owner-1")
	defer second.Close()
	waitType(t, second, ipc.MsgWorkspaceState, 3*time.Second)
	if h.d.clientCount() != 1 {
		t.Fatalf("clients=%d, want the dead record replaced", h.d.clientCount())
	}
	finishDispatch(t, first)
	finishDispatch(t, second)
}

func attachTestClientID(t *testing.T, sock, id string) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	attachOn(t, c, id)
	return c
}
