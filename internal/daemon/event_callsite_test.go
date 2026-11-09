package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// The emitter tests beside this file call notifyMCPControl and friends
// directly. That proves the helpers work and says nothing about whether any
// handler reaches them — mutation testing confirmed it: every gate in this file
// could be replaced with `false`, and every call site deleted, without a single
// red test.
//
// These drive the HANDLERS and assert on the queue.

// callsiteDaemon builds a WHOLE daemon — New, not a struct literal — with one
// real pane registered in its session.
//
// These drive complete handlers, which reach broadcast, snapshot and cleanup
// paths that a three-field literal cannot satisfy; a partial daemon nil-panics
// inside handleDestroyPaneReq rather than failing an assertion. There is no IPC
// server, so every broadcast is the documented nil-server no-op.
func callsiteDaemon(t *testing.T) (*Daemon, *Pane) {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	return d, pane
}

// withHealthyPTY gives a pane a PTY whose writes never block, so
// paneInputOutcome reports Delivered. Without one the input is refused for
// "pane has no process" — which is correct, and is what the negative case below
// exercises deliberately.
func withHealthyPTY(t *testing.T, pane *Pane) {
	t.Helper()
	w := newWedgedSession()
	close(w.release) // healthy: Write returns immediately
	pane.PluginMu.Lock()
	pane.PTY = w
	pane.PluginMu.Unlock()
	t.Cleanup(pane.StopInput)
}

// bridgeConn returns a conn the registry knows as an MCP bridge.
func bridgeConn(d *Daemon) *ipc.Conn {
	c := &ipc.Conn{}
	d.hellos.put(c, ipc.ClientHelloPayload{Role: "bridge"})
	return c
}

// tuiConn returns a conn the registry knows as the TUI.
func tuiConn(d *Daemon) *ipc.Conn {
	c := &ipc.Conn{}
	d.hellos.put(c, ipc.ClientHelloPayload{Role: "tui"})
	return c
}

func inputMsg(t *testing.T, paneID string, data []byte) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: paneID, Data: data})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// A bridge typing into a pane raises a card; the TUI doing the same does not.
// Both halves matter — the gate could be inverted or removed and the emitter
// tests would not notice.
func TestHandlePaneInput_CardsOnlyForABridge(t *testing.T) {
	t.Run("bridge", func(t *testing.T) {
		d, pane := callsiteDaemon(t)
		withHealthyPTY(t, pane)
		d.handlePaneInput(bridgeConn(d), inputMsg(t, pane.ID, []byte("ls\n")))

		evs := d.events.Events()
		if len(evs) != 1 {
			t.Fatalf("events after a bridge wrote: got %d, want 1", len(evs))
		}
		if evs[0].Type != "mcp_control" {
			t.Errorf("type: got %q, want %q", evs[0].Type, "mcp_control")
		}
		if evs[0].PaneID != pane.ID {
			t.Errorf("pane id: got %q, want %q", evs[0].PaneID, pane.ID)
		}
	})

	t.Run("tui", func(t *testing.T) {
		d, pane := callsiteDaemon(t)
		withHealthyPTY(t, pane)
		d.handlePaneInput(tuiConn(d), inputMsg(t, pane.ID, []byte("ls\n")))

		if got := d.events.Count(); got != 0 {
			t.Errorf("events after the TUI wrote: got %d, want 0", got)
		}
	})
}

// A card must never claim an agent typed into a pane whose input the daemon
// refused. A pane with no process is one of the four refusals.
func TestHandlePaneInput_NoCardWhenTheInputWasNotDelivered(t *testing.T) {
	d, _ := callsiteDaemon(t)

	d.handlePaneInput(bridgeConn(d), inputMsg(t, "pane-does-not-exist", []byte("ls\n")))

	if got := d.events.Count(); got != 0 {
		t.Errorf("events for refused input: got %d, want 0", got)
	}
}

func updatePaneMsg(t *testing.T, paneID string, pinned *bool, marked *bool) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID:            paneID,
		PinnedAttention:   pinned,
		MarkedForDeletion: marked,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// The change detection, pinned in BOTH directions. A client may re-send the
// mark it already holds, and a card per re-send is the repeat telemetry this
// whole feature exists to remove — but `changed := true` was equally invisible.
func TestHandleUpdatePane_MarkCardsOnlyOnARealChange(t *testing.T) {
	d, pane := callsiteDaemon(t)
	yes, no := true, false
	conn := tuiConn(d)

	d.handleUpdatePane(conn, updatePaneMsg(t, pane.ID, &yes, nil))
	if got := d.events.Count(); got != 1 {
		t.Fatalf("events after pinning: got %d, want 1", got)
	}
	if evs := d.events.Events(); evs[0].Type != "pane_pinned" {
		t.Errorf("type: got %q, want %q", evs[0].Type, "pane_pinned")
	}

	// Re-sending the same value is not a change.
	d.handleUpdatePane(conn, updatePaneMsg(t, pane.ID, &yes, nil))
	if got := d.events.Count(); got != 1 {
		t.Errorf("events after re-sending the same pin: got %d, want 1", got)
	}

	// Flipping it is.
	d.handleUpdatePane(conn, updatePaneMsg(t, pane.ID, &no, nil))
	if got := d.events.Count(); got != 2 {
		t.Fatalf("events after unpinning: got %d, want 2", got)
	}
	found := false
	for _, e := range d.events.Events() {
		if e.Type == "pane_unpinned" {
			found = true
		}
	}
	if !found {
		t.Error("no pane_unpinned event after the pin was cleared")
	}
}

// The deletion mark is the pin's mirror image and had the same unpinned gate.
func TestHandleUpdatePane_DeletionMarkCardsOnlyOnARealChange(t *testing.T) {
	d, pane := callsiteDaemon(t)
	yes := true
	conn := tuiConn(d)

	d.handleUpdatePane(conn, updatePaneMsg(t, pane.ID, nil, &yes))
	d.handleUpdatePane(conn, updatePaneMsg(t, pane.ID, nil, &yes))

	evs := d.events.Events()
	if len(evs) != 1 {
		t.Fatalf("events after marking twice: got %d, want 1", len(evs))
	}
	if evs[0].Type != "pane_marked_deletion" {
		t.Errorf("type: got %q, want %q", evs[0].Type, "pane_marked_deletion")
	}
}

// A payload carrying neither mark — an OSC 7 CWD update, which the panes
// holding a deletion mark emit constantly — must produce no card at all.
func TestHandleUpdatePane_NoMarkFieldsMeansNoCard(t *testing.T) {
	d, pane := callsiteDaemon(t)
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID: pane.ID,
		CWD:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	d.handleUpdatePane(tuiConn(d), msg)

	if got := d.events.Count(); got != 0 {
		t.Errorf("events for a CWD-only update: got %d, want 0", got)
	}
}

// handleDestroyPaneReq is NOT driven here, deliberately.
//
// It answers with respondTo, and this package cannot build an `ipc.Conn` that
// survives a send: the constructor is unexported, and a zero-value Conn panics
// closing its nil channels the moment the response fails. No daemon test builds
// one today. What that leaves untested is the WIRING of roleOf to the actor
// string on that one path — roleOf itself, the actor mapping and the emitter
// are each covered, and the sibling handleDestroyPane path below is driven end
// to end.
//
// Closing it properly means an exported test seam on ipc.Conn, which is a
// change to the transport rather than to this feature.

// The TUI's own destroy path emits too, and labels the user.
func TestHandleDestroyPane_EmitsForTheUser(t *testing.T) {
	d, pane := callsiteDaemon(t)
	msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	d.handleDestroyPane(nil, msg)

	var destroyed *PaneEvent
	for _, e := range d.events.Events() {
		if e.Type == "pane_destroyed" {
			cp := e
			destroyed = &cp
		}
	}
	if destroyed == nil {
		t.Fatalf("no pane_destroyed event; queue = %+v", d.events.Events())
	}
	if destroyed.Data["by"] != "user" {
		t.Errorf("Data[by]: got %q, want %q", destroyed.Data["by"], "user")
	}
}
