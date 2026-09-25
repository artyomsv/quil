package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// attachClientAs dials the daemon and attaches as the client id at a RAW
// window size, like a TUI does.
func attachClientAs(t *testing.T, sock, id string, cols, rows int) *ipc.Client {
	t.Helper()
	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	sendClientMsg(t, c, ipc.MsgAttach, ipc.AttachPayload{ClientID: id, Cols: cols, Rows: rows})
	return c
}

func sendClientMsg(t *testing.T, c *ipc.Client, typ string, payload any) {
	t.Helper()
	msg, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatalf("build %s: %v", typ, err)
	}
	if err := c.Send(msg); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}
}

// clientRecordByID reads one client's record by id, for tests that hold only
// the client side of a conn.
func clientRecordByID(d *Daemon, id string) (clientRecord, bool) {
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	for _, rec := range d.clients.byConn {
		if rec.id == id {
			cp := *rec
			return cp, true
		}
	}
	return clientRecord{}, false
}

// Proves the dispatch arms are WIRED: every step goes over a real socket, so a
// missing case arm or a stamp in the wrong arm fails here even when the
// registry's own tests pass.
func TestClientDispatch_ArmsAreWired(t *testing.T) {
	d, sock := overlayServerDaemon(t)

	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A registered", func() bool { return d.masterID() == "A" })
	b := attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B registered", func() bool { return d.clientCount() == 2 })
	if d.masterID() != "A" {
		t.Fatalf("masterID = %q, want A (the oldest paintable client)", d.masterID())
	}

	// pane_input stamps lastInputAt even when the pane does not exist: the
	// user typed, and the stamp is about the client, not the delivery.
	if rec, _ := clientRecordByID(d, "B"); !rec.lastInputAt.IsZero() {
		t.Fatal("B has input before it typed anything")
	}
	sendClientMsg(t, b, ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: "pane-none", Data: []byte("x")})
	waitUntil(t, "B's lastInputAt stamped", func() bool {
		rec, _ := clientRecordByID(d, "B")
		return !rec.lastInputAt.IsZero()
	})
	if rec, _ := clientRecordByID(d, "A"); !rec.lastInputAt.IsZero() {
		t.Error("B's input stamped A")
	}

	sendClientMsg(t, b, ipc.MsgTakeControl, nil)
	waitUntil(t, "take_control makes B master", func() bool { return d.masterID() == "B" })

	sendClientMsg(t, b, ipc.MsgClientGeometry, ipc.ClientGeometryPayload{Cols: 0, Rows: 0})
	waitUntil(t, "B going unpaintable hands back to A", func() bool { return d.masterID() == "A" })

	sendClientMsg(t, a, ipc.MsgDetach, nil)
	waitUntil(t, "A's detach removes it at once", func() bool { return d.clientCount() == 1 })
	if d.masterID() != "" {
		t.Errorf("masterID = %q, want none: B is unpaintable and A detached", d.masterID())
	}
	if !d.sizeAuthorityOpen() {
		t.Error("a detach must leave no reserved slot")
	}
}

// The attach geometry the registry records is the RAW one. handleAttach
// defaults clientSize to 80x24 for a 0x0 attach, and eligibility must not see
// that default: it is the 1x1 incident coming back through a new door.
func TestClientDispatch_ZeroAttachNeverElectedDespiteDefault(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	attachClientAs(t, sock, "headless", 0, 0)
	waitUntil(t, "client registered", func() bool { return d.clientCount() == 1 })
	waitUntil(t, "clientSize defaulted", func() bool { return d.clientSize.Load() != nil })

	if sz := d.clientSize.Load(); sz.cols != 80 || sz.rows != 24 {
		t.Fatalf("clientSize = %dx%d, want the 80x24 default", sz.cols, sz.rows)
	}
	if d.masterID() != "" {
		t.Errorf("masterID = %q, want none for a 0x0 attach", d.masterID())
	}
	rec, _ := clientRecordByID(d, "headless")
	if rec.cols != 0 || rec.rows != 0 {
		t.Errorf("recorded geometry = %dx%d, want the raw 0x0", rec.cols, rec.rows)
	}
}

// An attach that changes the master tells every OTHER attached client, and
// sends the attaching client only its own state. That state already names the
// new master; a broadcast on top of it was a second frame with nothing new.
func TestClientDispatch_AttachMasterChangeReachesOthersOnly(t *testing.T) {
	d, sock, _ := resizeAuthorityDaemon(t)
	small := attachClientAs(t, sock, "small", 0, 0)
	readUntil(t, small, "small's attach state", isType(ipc.MsgWorkspaceState))
	if d.masterID() != "" {
		t.Fatalf("masterID = %q, want none for a 0x0 client", d.masterID())
	}

	big := attachClientAs(t, sock, "big", 200, 50)
	readUntil(t, small, "a state naming big the master", func(m *ipc.Message) bool {
		if m.Type != ipc.MsgWorkspaceState {
			return false
		}
		var s map[string]any
		return m.DecodePayload(&s) == nil && s["size_master"] == "big"
	})
	if n := countType(readFor(big, 300*time.Millisecond), ipc.MsgWorkspaceState); n != 1 {
		t.Errorf("the attaching client received %d workspace_state frames, want only its own 1", n)
	}
}

// A lost link through the real server keeps the master's slot while a
// follower is attached.
func TestClientDispatch_LostLinkReservesSlot(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	// Fake timers, so no real grace timer outlives the test. Installed before
	// any client attaches; nothing below reads the harness's own fields.
	(&clientsHarness{t: t}).install(d, testGrace)
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A master", func() bool { return d.masterID() == "A" })
	attachClientAs(t, sock, "B", 100, 30)
	waitUntil(t, "B registered", func() bool { return d.clientCount() == 2 })

	a.Close()
	waitUntil(t, "A's disconnect processed", func() bool { return d.clientCount() == 1 })
	if d.masterID() != "A" || d.sizeAuthorityOpen() {
		t.Errorf("masterID = %q, open = %v; want A's slot reserved", d.masterID(), d.sizeAuthorityOpen())
	}
}
