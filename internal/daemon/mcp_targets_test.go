package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// Multi-client sync: targeting one client with an MCP command that used to
// broadcast to every attached TUI, per-client default directories, and the
// shared dismiss/seen marks. Every test drives real conns through
// ipc.Server, for the reason clients_wiring_test.go gives: the gate reads
// which CONN sent or should receive a message, and a direct handler call has
// no conn to read.

// attachClientWithCWD attaches like attachClientAs but also carries a CWD,
// for defaultCWD's per-client candidate.
func attachClientWithCWD(t *testing.T, sock, id string, cols, rows int, cwd string) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	sendClientMsg(t, c, ipc.MsgAttach, ipc.AttachPayload{ClientID: id, Cols: cols, Rows: rows, CWD: cwd})
	return c
}

// dialBridge dials the socket without ever attaching — an MCP bridge's shape:
// connected, but not a client the registry counts.
func dialBridge(t *testing.T, sock string) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// resolvedTemp returns a real temp directory, symlink-resolved the same way
// resolveSpawnDirWithin resolves it — t.TempDir() on some platforms lives
// under a symlink, and defaultCWD's candidates are always compared in
// resolved form.
func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

// readUntilID reads c's frames — COLLECTING every one — until a response of
// respType with the given envelope id arrives, with NO SetReadDeadline.
//
// readFor and readUntil (resize_authority_test.go) both call
// SetReadDeadline, and ipc.ReadMessage's io.ReadFull DISCARDS whatever
// partial length-prefix or payload bytes it already consumed the instant
// that deadline fires mid-read — the next call on the same conn then
// misreads leftover bytes as a fresh frame header. That is fine for a
// single terminal read, but a test that calls a deadline-based reader
// MORE THAN ONCE on the same conn risks exactly that corruption on a slow
// CI run. This helper is safe to call repeatedly on one conn: each call's
// background goroutine reads until ITS match (or the conn closes, or the
// caller's own timeout fires), and two calls never race because the first
// one's goroutine has already returned by the time the caller sees its
// result.
func readUntilID(t *testing.T, c *ipc.Client, respType, id string, within time.Duration) []*ipc.Message {
	t.Helper()
	type result struct {
		got []*ipc.Message
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var got []*ipc.Message
		for {
			m, err := c.Receive()
			if err != nil {
				ch <- result{got, err}
				return
			}
			got = append(got, m)
			if m.Type == respType && m.ID == id {
				ch <- result{got, nil}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("waiting for %s(%s): %v", respType, id, r.err)
		}
		return r.got
	case <-time.After(within):
		t.Fatalf("timed out waiting for %s(%s)", respType, id)
		return nil
	}
}

// sendWithID sends payload as typ on c, stamping the envelope id so a
// caller can correlate it with the resulting pane_op_resp/etc via
// readUntilID.
func sendWithID(t *testing.T, c *ipc.Client, typ, id string, payload any) {
	t.Helper()
	msg, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatalf("build %s: %v", typ, err)
	}
	msg.ID = id
	if err := c.Send(msg); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}
}

// workspaceStateActiveTab decodes a workspace_state frame's top-level
// active_tab field.
func workspaceStateActiveTab(t *testing.T, m *ipc.Message) string {
	t.Helper()
	var s struct {
		ActiveTab string `json:"active_tab"`
	}
	if err := m.DecodePayload(&s); err != nil {
		t.Fatal(err)
	}
	return s.ActiveTab
}

// sawActiveTab reports whether any workspace_state frame in msgs names
// tabID as the active tab.
func sawActiveTab(t *testing.T, msgs []*ipc.Message, tabID string) bool {
	t.Helper()
	for _, m := range msgs {
		if m.Type == ipc.MsgWorkspaceState && workspaceStateActiveTab(t, m) == tabID {
			return true
		}
	}
	return false
}

// TestCloseTUI_ReachesMostRecentlyActiveOnly: three attached clients. C is
// the NEWEST attached, but A — the OLDEST — types LAST, so the implicit
// target must be A. This is what proves the choice is driven by input
// recency and not merely by "the newest attached client" (which happens to
// coincide with "typed last" unless a test goes out of its way to separate
// them).
func TestCloseTUI_ReachesMostRecentlyActiveOnly(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T") // skip the real-PTY default workspace on first attach

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	c := attachClientAs(t, sock, "C", 100, 30)
	waitUntil(t, "C attached", func() bool { return d.clientCount() == 3 }) // C is newest attached

	barrier(t, d, b, "B")
	barrier(t, d, c, "C")
	barrier(t, d, a, "A") // A — the OLDEST attached — types last

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgCloseTUI, nil)

	aGot := readFor(a, 500*time.Millisecond)
	if countType(aGot, ipc.MsgCloseTUI) != 1 {
		t.Fatalf("A (typed last, though oldest attached) got close_tui %d times, want 1: %v", countType(aGot, ipc.MsgCloseTUI), aGot)
	}
	bGot := readFor(b, 200*time.Millisecond)
	if n := countType(bGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("B got close_tui %d times, want 0", n)
	}
	cGot := readFor(c, 200*time.Millisecond)
	if n := countType(cGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("C (newest attached, but did not type last) got close_tui %d times, want 0", n)
	}
}

// TestCloseTUI_NobodyTypedYetReachesOldestAttached: with no input at all,
// the implicit target is the OLDEST attached client — not the newest, which
// is mostRecentlyActiveConn's OWN "nobody typed" answer (see targetConn's
// doc comment for why the two deliberately disagree there).
func TestCloseTUI_NobodyTypedYetReachesOldestAttached(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	c := attachClientAs(t, sock, "C", 100, 30)
	waitUntil(t, "C attached", func() bool { return d.clientCount() == 3 })
	// Nobody has sent any input.

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgCloseTUI, nil)

	aGot := readFor(a, 500*time.Millisecond)
	if countType(aGot, ipc.MsgCloseTUI) != 1 {
		t.Fatalf("A (oldest attached, nobody has typed) got close_tui %d times, want 1: %v", countType(aGot, ipc.MsgCloseTUI), aGot)
	}
	bGot := readFor(b, 200*time.Millisecond)
	if n := countType(bGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("B got close_tui %d times, want 0", n)
	}
	cGot := readFor(c, 200*time.Millisecond)
	if n := countType(cGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("C got close_tui %d times, want 0", n)
	}
}

// TestCloseTUI_ExplicitClient: an explicit Client always wins over the
// implicit most-recently-active target.
func TestCloseTUI_ExplicitClient(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	// B is the most-recently-active client (typed after A attached).
	barrier(t, d, a, "A")
	barrier(t, d, b, "B")

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgCloseTUI, ipc.CloseTUIPayload{Client: "A"})

	aGot := readFor(a, 500*time.Millisecond)
	if countType(aGot, ipc.MsgCloseTUI) != 1 {
		t.Fatalf("A (explicit target) got close_tui %d times, want 1: %v", countType(aGot, ipc.MsgCloseTUI), aGot)
	}
	bGot := readFor(b, 200*time.Millisecond)
	if n := countType(bGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("B got close_tui %d times, want 0 (A was named explicitly)", n)
	}
}

// TestSetActivePane_FocusFrameToOneConn: the tab-switch broadcast reaches
// every attached conn, and the set_active_pane focus frame reaches only the
// named client. The workspace is switched to a DIFFERENT tab before either
// client attaches, so a workspace_state naming tab.ID as active can only be
// the broadcast this set_active_pane triggers — never attach-time noise —
// which lets this test read each conn exactly ONCE (no drain needed).
func TestSetActivePane_FocusFrameToOneConn(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	other := d.session.CreateTab("Other")
	d.session.SwitchTab(other.ID)

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgSetActivePane, ipc.SetActivePanePayload{PaneID: pane.ID, Client: "B"})

	bGot := readFor(b, 500*time.Millisecond)
	if countType(bGot, ipc.MsgSetActivePane) != 1 {
		t.Fatalf("B (named client) got set_active_pane %d times, want 1: %v", countType(bGot, ipc.MsgSetActivePane), bGot)
	}
	if !sawActiveTab(t, bGot, tab.ID) {
		t.Error("B never saw the tab-switch broadcast (no workspace_state named tab.ID active)")
	}
	aGot := readFor(a, 300*time.Millisecond)
	if n := countType(aGot, ipc.MsgSetActivePane); n != 0 {
		t.Errorf("A got set_active_pane %d times, want 0", n)
	}
	if !sawActiveTab(t, aGot, tab.ID) {
		t.Error("A (not the named client) never saw the tab-switch broadcast — it must reach every attached conn")
	}
}

// TestMCPTargets_NoAttachedClient: Review Focus 4. A headless daemon (no
// attached client at all) must not panic on any of these: close_tui sends
// nothing, set_active_pane only switches the tab, and create_pane_req with
// an empty CWD falls all the way back to os.Getwd(). The bridge conn that
// SENT close_tui/set_active_pane is itself read afterward to confirm the
// daemon did not echo either command back to its own sender.
func TestMCPTargets_NoAttachedClient(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	other := d.session.CreateTab("Other")
	d.session.SwitchTab(other.ID) // so switching back to tab.ID below is an observable change

	bridge := dialBridge(t, sock)

	sendClientMsg(t, bridge, ipc.MsgCloseTUI, nil)
	sendClientMsg(t, bridge, ipc.MsgSetActivePane, ipc.SetActivePanePayload{PaneID: pane.ID})
	waitUntil(t, "the tab switched with nobody attached", func() bool {
		return d.session.ActiveTabID() == tab.ID
	})

	// Nobody is attached to receive either command, and the sender itself
	// (an MCP bridge, never attached) must not have it echoed back either.
	bridgeGot := readFor(bridge, 300*time.Millisecond)
	if n := countType(bridgeGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("close_tui echoed back to its own sender %d times, want 0", n)
	}
	if n := countType(bridgeGot, ipc.MsgSetActivePane); n != 0 {
		t.Errorf("set_active_pane echoed back to its own sender %d times, want 0", n)
	}
	// readFor's SetReadDeadline is still armed and has already elapsed —
	// clear it, or the roundTrip calls below inherit that stale deadline and
	// fail with a spurious i/o timeout on their very first Receive.
	if err := bridge.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline: %v", err)
	}

	hostCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, bridge, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
		ipc.CreatePaneReqPayload{TabID: tab.ID}))
	if resp.Error != "" {
		t.Fatalf("create_pane_req: %s", resp.Error)
	}
	created := d.session.Pane(resp.PaneID)
	if created == nil {
		t.Fatal("pane not created")
	}
	created.PluginMu.Lock()
	gotCWD := created.CWD
	created.PluginMu.Unlock()
	if gotCWD != hostCWD {
		t.Errorf("headless create_pane_req CWD = %q, want os.Getwd() %q", gotCWD, hostCWD)
	}

	// Neither command may have wedged the daemon: it must still answer.
	// roundTrip itself fails the test if no response arrives in time.
	roundTrip(t, bridge, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
}

// TestDefaultCWD_PerClientAndBridge: each attached client gets ITS OWN
// default directory, and a bridge conn (no attach at all) falls back to the
// size master's directory. Driven through handleBrowseDirReq — an empty Path
// asks for defaultCWD(conn) and echoes it back as Resolved, with no PTY
// spawn.
func TestDefaultCWD_PerClientAndBridge(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")

	dirA := resolvedTemp(t)
	dirB := resolvedTemp(t)

	a := attachClientWithCWD(t, sock, "A", 200, 50, dirA)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientWithCWD(t, sock, "B", 100, 30, dirB)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	if d.masterID() != "A" {
		t.Fatalf("masterID = %q, want A (the oldest eligible client)", d.masterID())
	}

	if got := decodeInto[ipc.BrowseDirRespPayload](t, roundTrip(t, a, ipc.MsgBrowseDirReq, ipc.MsgBrowseDirResp, ipc.BrowseDirReqPayload{})); got.Resolved != dirA {
		t.Errorf("A's default dir = %q, want its own cwd %q", got.Resolved, dirA)
	}
	if got := decodeInto[ipc.BrowseDirRespPayload](t, roundTrip(t, b, ipc.MsgBrowseDirReq, ipc.MsgBrowseDirResp, ipc.BrowseDirReqPayload{})); got.Resolved != dirB {
		t.Errorf("B's default dir = %q, want its own cwd %q", got.Resolved, dirB)
	}

	bridge := dialBridge(t, sock)
	if got := decodeInto[ipc.BrowseDirRespPayload](t, roundTrip(t, bridge, ipc.MsgBrowseDirReq, ipc.MsgBrowseDirResp, ipc.BrowseDirReqPayload{})); got.Resolved != dirA {
		t.Errorf("a bridge's default dir = %q, want the master's (A's) cwd %q", got.Resolved, dirA)
	}
}

// TestCreatePaneReq_FromNonMasterClientUsesItsOwnCWD (Important 3): the
// CREATE path — not just the read-only browse path above — resolves against
// the REQUESTING conn's own cwd, even when that conn is not the size
// master. B is a follower here (A, the oldest attached, is master with
// dirA); a create sent on B's own conn must land in dirB, not in A's.
func TestCreatePaneReq_FromNonMasterClientUsesItsOwnCWD(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")

	dirA := resolvedTemp(t)
	dirB := resolvedTemp(t)
	attachClientWithCWD(t, sock, "A", 200, 50, dirA)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientWithCWD(t, sock, "B", 100, 30, dirB)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	if d.masterID() != "A" {
		t.Fatalf("masterID = %q, want A", d.masterID())
	}

	resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, b, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
		ipc.CreatePaneReqPayload{TabID: tab.ID}))
	if resp.Error != "" {
		t.Fatalf("create_pane_req: %s", resp.Error)
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatal("pane not created")
	}
	pane.PluginMu.Lock()
	gotCWD := pane.CWD
	pane.PluginMu.Unlock()
	if gotCWD != dirB {
		t.Errorf("create from B (a follower) landed in %q, want its own cwd %q (not A's master cwd %q)", gotCWD, dirB, dirA)
	}
}

// TestDefaultCWD_SameClientProbedOnce (Important 1): in the single-TUI case
// the conn, master and most-recently-active steps all name the SAME client,
// so a dead directory must be probed exactly once — not three times over,
// each paying its own spawnDirProbeTimeout and abandoning its own
// claimBlockingFSCall permit.
func TestDefaultCWD_SameClientProbedOnce(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")

	var calls atomic.Int64
	block := make(chan struct{})
	orig := statPath
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-block
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { statPath = orig }) })

	attachClientWithCWD(t, sock, "A", 200, 50, "/mnt/dead-share/work")
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	master := d.masterConn()
	if master == nil {
		t.Fatal("A did not become master")
	}

	done := make(chan string, 1)
	go func() { done <- d.defaultCWD(master) }()

	select {
	case got := <-done:
		if got == "/mnt/dead-share/work" {
			t.Errorf("defaultCWD returned the unreachable path %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("defaultCWD did not return within 10s — probably retrying the same dead path serially")
	}

	if n := calls.Load(); n != 1 {
		t.Errorf("statPath called %d times, want exactly 1 (conn, master and most-recently-active all name the same client)", n)
	}
}

// TestDefaultCWD_DedupesIdenticalPathsEvenWithBudgetToSpare isolates the
// DEDUP half of the Important-1 fix from the shared-deadline half above: a
// FAST-failing stat leaves the shared deadline almost entirely unspent, so
// only the "skip a candidate already tried" check — not the deadline
// running out — can be what stops a second and third identical probe here.
func TestDefaultCWD_DedupesIdenticalPathsEvenWithBudgetToSpare(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")

	var calls atomic.Int64
	orig := statPath
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { statPath = orig })

	attachClientWithCWD(t, sock, "A", 200, 50, "/mnt/dead-share/work")
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	master := d.masterConn()
	if master == nil {
		t.Fatal("A did not become master")
	}

	got := d.defaultCWD(master)
	if got == "/mnt/dead-share/work" {
		t.Errorf("defaultCWD returned the unreachable path %q", got)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("statPath called %d times, want exactly 1 — the conn, master and most-recently-active steps all name A's own path", n)
	}
}

// TestDefaultCWD_SharesOneDeadlineAcrossDifferentDeadCandidates isolates the
// SHARED-DEADLINE half: three attached clients with three DIFFERENT
// unreachable directories (so dedup-by-path cannot collapse them) must still
// cost this call no more than roughly ONE spawnDirProbeTimeout, not three
// paid serially.
func TestDefaultCWD_SharesOneDeadlineAcrossDifferentDeadCandidates(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")

	block := make(chan struct{})
	orig := statPath
	statPath = func(string) (os.FileInfo, error) {
		<-block
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { statPath = orig }) })

	attachClientWithCWD(t, sock, "A", 200, 50, "/mnt/dead-a") // oldest attached: becomes master
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	attachClientWithCWD(t, sock, "B", 100, 30, "/mnt/dead-b")
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	c := attachClientWithCWD(t, sock, "C", 100, 30, "/mnt/dead-c")
	waitUntil(t, "C attached", func() bool { return d.clientCount() == 3 })
	barrier(t, d, c, "C") // C is now the most-recently-active client

	// The requesting conn is B: neither the master (A) nor the
	// most-recently-active client (C), so all three steps of defaultCWD name
	// three DIFFERENT dead directories.
	rec, ok := clientRecordByID(d, "B")
	if !ok {
		t.Fatal("B not found in the registry")
	}

	start := time.Now()
	got := d.defaultCWD(rec.conn)
	elapsed := time.Since(start)

	for _, dead := range []string{"/mnt/dead-a", "/mnt/dead-b", "/mnt/dead-c"} {
		if got == dead {
			t.Errorf("defaultCWD returned the unreachable path %q", got)
		}
	}
	// Worst case without a shared deadline is 3 * spawnDirProbeTimeout (6s);
	// this bound sits well below that and comfortably above the ~1x a shared
	// deadline costs, so it separates the two without being timing-fragile.
	if elapsed > 3*time.Second {
		t.Errorf("defaultCWD took %s across 3 different dead candidates, want well under 3x spawnDirProbeTimeout (the deadline must be shared)", elapsed)
	}
}

// TestDismiss_BroadcastsEventDismissed: a dismissal reaches every attached
// client, so a card dismissed through one TUI's sidebar disappears from a
// second one too. event_dismissed never appears as ordinary attach noise, so
// one read after the send is enough.
func TestDismiss_BroadcastsEventDismissed(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgDismissEvent, ipc.DismissEventPayload{EventID: "evt-1"})

	got := readFor(a, 500*time.Millisecond)
	var found *ipc.EventDismissedPayload
	for _, m := range got {
		if m.Type == ipc.MsgEventDismissed {
			var p ipc.EventDismissedPayload
			if err := m.DecodePayload(&p); err != nil {
				t.Fatal(err)
			}
			found = &p
		}
	}
	if found == nil {
		t.Fatalf("no event_dismissed reached A: %v", got)
	}
	if found.EventID != "evt-1" {
		t.Errorf("EventDismissedPayload.EventID = %q, want %q", found.EventID, "evt-1")
	}
}

// TestPaneSeen_OnlyOnTrueToFalse: false→false and true→true send no
// pane_seen frame; only a true→false transition does, exactly once.
//
// Each update_pane is sent WITH an envelope id and checked via readUntilID,
// which waits for that update's own pane_op_resp (sent, unconditionally,
// AFTER handleUpdatePane returns — so any pane_seen broadcast the same
// update triggered is already queued ahead of it on this same conn). That
// makes three checkpoints on ONE conn safe: readUntilID sets no read
// deadline, unlike calling readFor three times over on the same conn.
func TestPaneSeen_OnlyOnTrueToFalse(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })

	setUnseen := func(v bool) {
		pane.PluginMu.Lock()
		pane.Unseen = v
		pane.PluginMu.Unlock()
	}
	step := 0
	sendUnseen := func(v bool) []*ipc.Message {
		step++
		id := fmt.Sprintf("pane-seen-step-%d", step)
		sendWithID(t, a, ipc.MsgUpdatePane, id, ipc.UpdatePanePayload{PaneID: pane.ID, Unseen: &v})
		return readUntilID(t, a, ipc.MsgPaneOpResp, id, 3*time.Second)
	}

	setUnseen(false)
	if got := countType(sendUnseen(false), ipc.MsgPaneSeen); got != 0 {
		t.Errorf("false->false sent pane_seen %d times, want 0", got)
	}

	setUnseen(true)
	if got := countType(sendUnseen(true), ipc.MsgPaneSeen); got != 0 {
		t.Errorf("true->true sent pane_seen %d times, want 0", got)
	}

	setUnseen(true)
	msgs := sendUnseen(false)
	if got := countType(msgs, ipc.MsgPaneSeen); got != 1 {
		t.Fatalf("true->false sent pane_seen %d times, want 1: %v", got, msgs)
	}
	for _, m := range msgs {
		if m.Type != ipc.MsgPaneSeen {
			continue
		}
		var p ipc.PaneSeenPayload
		if err := m.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if p.PaneID != pane.ID {
			t.Errorf("PaneSeenPayload.PaneID = %q, want %q", p.PaneID, pane.ID)
		}
	}
}

// TestListClients_Fields covers list_clients_req's field-level contract: the
// oldest client first, Master set on exactly the size master, empty
// LastInputAt for a client that has not typed, and a populated one — parsing
// as RFC 3339 — for one that has.
func TestListClients_Fields(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })

	resp := decodeInto[ipc.ListClientsRespPayload](t, roundTrip(t, a, ipc.MsgListClientsReq, ipc.MsgListClientsResp, nil))
	if len(resp.Clients) != 2 {
		t.Fatalf("clients = %+v, want 2", resp.Clients)
	}
	if resp.Clients[0].Client != "A" || !resp.Clients[0].Master {
		t.Errorf("clients[0] = %+v, want A as master (oldest attached)", resp.Clients[0])
	}
	if resp.Clients[1].Client != "B" || resp.Clients[1].Master {
		t.Errorf("clients[1] = %+v, want B, not master", resp.Clients[1])
	}
	for _, c := range resp.Clients {
		if c.LastInputAt != "" {
			t.Errorf("client %s LastInputAt = %q, want empty before any input", c.Client, c.LastInputAt)
		}
		if _, err := time.Parse(time.RFC3339, c.AttachedAt); err != nil {
			t.Errorf("client %s AttachedAt = %q, not RFC 3339: %v", c.Client, c.AttachedAt, err)
		}
	}

	barrier(t, d, b, "B")
	resp = decodeInto[ipc.ListClientsRespPayload](t, roundTrip(t, a, ipc.MsgListClientsReq, ipc.MsgListClientsResp, nil))
	byID := map[string]ipc.ClientInfo{}
	for _, c := range resp.Clients {
		byID[c.Client] = c
	}
	if byID["A"].LastInputAt != "" {
		t.Errorf("A LastInputAt = %q, want still empty", byID["A"].LastInputAt)
	}
	if byID["B"].LastInputAt == "" {
		t.Fatal("B LastInputAt is still empty after it typed")
	}
	if _, err := time.Parse(time.RFC3339, byID["B"].LastInputAt); err != nil {
		t.Errorf("B LastInputAt = %q, not RFC 3339: %v", byID["B"].LastInputAt, err)
	}
}

// TestUpdatePane_LastInputStampsOnlyUserFields is the carried-over Task 2
// review fix: update_pane also reports automatic changes (an OSC 7 CWD
// change, overlay visibility, the unseen mark), and only a field the user
// actually acted on may stamp the sending client's last-input time —
// otherwise a pane silently reporting its own CWD makes an idle client look
// like the one somebody is driving, which is exactly what targetConn's
// implicit fallback reads to pick a client. Covers every user-originated
// field (Name, Muted, Eager, PinnedAttention, MarkedForDeletion) and every
// automatic one (CWD, OverlayVisible, Unseen).
func TestUpdatePane_LastInputStampsOnlyUserFields(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })

	fenceMarker := 40 // eligible geometry, distinct from A's 200x50 attach
	fence := func() {
		t.Helper()
		fenceMarker++
		sendClientMsg(t, a, ipc.MsgClientGeometry, ipc.ClientGeometryPayload{Cols: fenceMarker, Rows: fenceMarker})
		waitUntil(t, "fence processed", func() bool {
			rec, _ := clientRecordByID(d, "A")
			return rec.cols == fenceMarker && rec.rows == fenceMarker
		})
	}
	clearStamp := func() {
		d.clients.mu.Lock()
		defer d.clients.mu.Unlock()
		for _, rec := range d.clients.byConn {
			if rec.id == "A" {
				rec.lastInputAt = time.Time{}
			}
		}
	}

	notStamped := func(name string, payload ipc.UpdatePanePayload) {
		t.Helper()
		clearStamp()
		payload.PaneID = pane.ID
		sendClientMsg(t, a, ipc.MsgUpdatePane, payload)
		// client_geometry never touches lastInputAt (see the dispatch table in
		// daemon.go), so fencing with it proves the update_pane above already
		// ran WITHOUT itself being able to stamp anything.
		fence()
		rec, _ := clientRecordByID(d, "A")
		if !rec.lastInputAt.IsZero() {
			t.Errorf("%s stamped lastInputAt, want it to stay unstamped", name)
		}
	}
	stamped := func(name string, payload ipc.UpdatePanePayload) {
		t.Helper()
		clearStamp()
		payload.PaneID = pane.ID
		sendClientMsg(t, a, ipc.MsgUpdatePane, payload)
		waitUntil(t, name+" stamps lastInputAt", func() bool {
			rec, _ := clientRecordByID(d, "A")
			return !rec.lastInputAt.IsZero()
		})
	}

	notStamped("CWD-only", ipc.UpdatePanePayload{CWD: t.TempDir()})
	notStamped("OverlayVisible-only", ipc.UpdatePanePayload{OverlayVisible: boolPtr(true)})
	notStamped("Unseen-only", ipc.UpdatePanePayload{Unseen: boolPtr(true)})

	stamped("Name", ipc.UpdatePanePayload{Name: "renamed"})
	stamped("Muted", ipc.UpdatePanePayload{Muted: boolPtr(true)})
	stamped("Eager", ipc.UpdatePanePayload{Eager: boolPtr(true)})
	stamped("PinnedAttention", ipc.UpdatePanePayload{PinnedAttention: boolPtr(true)})
	stamped("MarkedForDeletion", ipc.UpdatePanePayload{MarkedForDeletion: boolPtr(true)})
}
