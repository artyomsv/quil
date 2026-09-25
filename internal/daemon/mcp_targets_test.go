package daemon

import (
	"os"
	"path/filepath"
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

// TestCloseTUI_ReachesMostRecentlyActiveOnly: three attached clients, B typed
// last. Only B's conn receives close_tui; A and C get none.
func TestCloseTUI_ReachesMostRecentlyActiveOnly(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T") // skip the real-PTY default workspace on first attach

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	c := attachClientAs(t, sock, "C", 100, 30)
	waitUntil(t, "C attached", func() bool { return d.clientCount() == 2 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 3 })

	barrier(t, d, a, "A")
	barrier(t, d, c, "C")
	barrier(t, d, b, "B") // B typed most recently

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgCloseTUI, nil)

	bGot := readFor(b, 500*time.Millisecond)
	if countType(bGot, ipc.MsgCloseTUI) != 1 {
		t.Fatalf("B (most recently active) got close_tui %d times, want 1: %v", countType(bGot, ipc.MsgCloseTUI), bGot)
	}
	aGot := readFor(a, 200*time.Millisecond)
	if n := countType(aGot, ipc.MsgCloseTUI); n != 0 {
		t.Errorf("A got close_tui %d times, want 0", n)
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
// named client.
func TestSetActivePane_FocusFrameToOneConn(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B attached", func() bool { return d.clientCount() == 2 })
	// Drain each conn's own attach replay / other-client state frame before
	// the assertions below, so the counts are about THIS set_active_pane.
	readFor(a, 300*time.Millisecond)
	readFor(b, 300*time.Millisecond)

	bridge := dialBridge(t, sock)
	sendClientMsg(t, bridge, ipc.MsgSetActivePane, ipc.SetActivePanePayload{PaneID: pane.ID, Client: "B"})

	bGot := readFor(b, 500*time.Millisecond)
	if countType(bGot, ipc.MsgSetActivePane) != 1 {
		t.Fatalf("B (named client) got set_active_pane %d times, want 1: %v", countType(bGot, ipc.MsgSetActivePane), bGot)
	}
	if countType(bGot, ipc.MsgWorkspaceState) == 0 {
		t.Error("B never saw the tab-switch broadcast")
	}
	aGot := readFor(a, 200*time.Millisecond)
	if n := countType(aGot, ipc.MsgSetActivePane); n != 0 {
		t.Errorf("A got set_active_pane %d times, want 0", n)
	}
	if countType(aGot, ipc.MsgWorkspaceState) == 0 {
		t.Error("A (not the named client) never saw the tab-switch broadcast — it must reach every attached conn")
	}
}

// TestMCPTargets_NoAttachedClient: Review Focus 4. A headless daemon (no
// attached client at all) must not panic on any of these, close_tui sends
// nothing, set_active_pane only switches the tab, and create_pane_req with
// an empty CWD falls all the way back to os.Getwd().
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
// spawn — rather than through create_pane_req, whose behavior for the same
// resolver is separately covered by TestMCPTargets_NoAttachedClient.
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

// TestDismiss_BroadcastsEventDismissed: a dismissal reaches every attached
// client, so a card dismissed through one TUI's sidebar disappears from a
// second one too.
func TestDismiss_BroadcastsEventDismissed(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.CreateTab("T")
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	readFor(a, 300*time.Millisecond) // drain the attach state frame

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
func TestPaneSeen_OnlyOnTrueToFalse(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })
	readFor(a, 300*time.Millisecond) // drain the attach state frame

	setUnseen := func(v bool) {
		pane.PluginMu.Lock()
		pane.Unseen = v
		pane.PluginMu.Unlock()
	}
	sendUnseen := func(v bool) []*ipc.Message {
		sendClientMsg(t, a, ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: pane.ID, Unseen: &v})
		return readFor(a, 300*time.Millisecond)
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
// implicit fallback reads to pick a client.
func TestUpdatePane_LastInputStampsOnlyUserFields(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })

	fenceMarker := 40 // eligible geometry, and distinct from A's 200x50 attach
	fence := func() {
		t.Helper()
		fenceMarker++
		sendClientMsg(t, a, ipc.MsgClientGeometry, ipc.ClientGeometryPayload{Cols: fenceMarker, Rows: fenceMarker})
		waitUntil(t, "fence processed", func() bool {
			rec, _ := clientRecordByID(d, "A")
			return rec.cols == fenceMarker && rec.rows == fenceMarker
		})
	}

	notStamped := func(name string, payload ipc.UpdatePanePayload) {
		t.Helper()
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

	notStamped("CWD-only", ipc.UpdatePanePayload{CWD: t.TempDir()})
	notStamped("OverlayVisible-only", ipc.UpdatePanePayload{OverlayVisible: boolPtr(true)})
	notStamped("Unseen-only", ipc.UpdatePanePayload{Unseen: boolPtr(true)})

	sendClientMsg(t, a, ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: pane.ID, Name: "renamed"})
	waitUntil(t, "Name stamps lastInputAt", func() bool {
		rec, _ := clientRecordByID(d, "A")
		return !rec.lastInputAt.IsZero()
	})
}
