package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// paneInputOutcome is the pure decision handlePaneInput reports back when the
// request carries an ID. Driven directly here; the wiring into the handler is
// pinned by the socket test below.
func TestPaneInputOutcome_ReportsAPaneWithNoProcess(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PreparingWorktree = "feat/x"

	got := d.paneInputOutcome(ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("ls\n")})
	if got.Delivered {
		t.Error("delivered = true for a pane with no process — the input went nowhere")
	}
	if !strings.Contains(got.Error, "worktree") {
		t.Errorf("Error = %q, want it to name the checkout the pane is waiting on", got.Error)
	}
}

// A pane that never had a process and is not preparing anything still has to say
// so rather than report success.
func TestPaneInputOutcome_ReportsAPaneThatIsNotRunning(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.SpawnError = "worktree is gone: /wt/feat-x"

	got := d.paneInputOutcome(ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("ls\n")})
	if got.Delivered || got.Error == "" {
		t.Errorf("delivered=%v error=%q, want a refusal with a reason", got.Delivered, got.Error)
	}
}

func TestPaneInputOutcome_ReportsAnUnknownPane(t *testing.T) {
	d := newTestDaemon(t)
	got := d.paneInputOutcome(ipc.PaneInputPayload{PaneID: "pane-does-not-exist"})
	if got.Delivered || got.Error == "" {
		t.Errorf("delivered=%v error=%q, want a refusal for a pane that is not there", got.Delivered, got.Error)
	}
}

// A live pane reports delivery, or every MCP send would look like a failure.
func TestPaneInputOutcome_ReportsDeliveryToALivePane(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	got := d.paneInputOutcome(ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("ls\n")})
	if !got.Delivered {
		t.Errorf("delivered = false for a live pane (error %q)", got.Error)
	}
}

// The TUI sends pane_input with NO id, thousands of times a session, and must
// keep getting no answer: this is the keystroke hot path and every response is a
// frame on that client's must-deliver queue.
func TestHandlePaneInput_AnswersOnlyWhenTheRequestCarriesAnID(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	client := attachTestClient(t, sock)
	defer client.Close()

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	// Under PluginMu: this daemon has a LIVE IPC server, so its snapshot and
	// broadcast goroutines are already reading this field. The other tests in
	// this file write it bare because their daemon has no server and therefore
	// no concurrent reader — a distinction the race detector makes and a local
	// `dev.sh test` does not.
	pane.PluginMu.Lock()
	pane.PreparingWorktree = "feat/x"
	pane.PluginMu.Unlock()

	msg, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("x")})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.ID = "probe-1"
	if err := client.Send(msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no pane_input_resp arrived for an id-bearing request")
		default:
		}
		resp, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if resp.Type != ipc.MsgPaneInputResp {
			continue
		}
		var payload ipc.PaneInputRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Delivered {
			t.Error("delivered = true for a pane with no process")
		}
		if resp.ID != "probe-1" {
			t.Errorf("resp.ID = %q, want the request's id echoed", resp.ID)
		}
		return
	}
}

// And the NEGATIVE half, which is what the test above is named for and did not
// actually check: an ID-LESS pane_input must be answered with nothing at all.
//
// The guard is load-bearing rather than tidy. respondTo goes to critCh, the
// MUST-DELIVER queue, whose overflow does not drop the frame — it force-closes
// the client. The TUI sends one id-less pane_input per keystroke, so answering
// them would put one critical frame per keystroke back at the TUI, and a paste
// or a held key outruns sendLoop and disconnects it. That is the documented
// 2026-08-09 shape.
//
// Ordered by a SECOND, id-bearing request of another type rather than by a
// sleep: one conn's messages are dispatched sequentially, so the arrival of the
// second answer proves the first was already handled and produced nothing.
func TestHandlePaneInput_SendsNoAnswerWhenTheRequestHasNoID(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	client := attachTestClient(t, sock)
	defer client.Close()

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PluginMu.Lock()
	pane.PreparingWorktree = "feat/x" // guarantees a refusal, so an answer WOULD be sent
	pane.PluginMu.Unlock()

	bare, err := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("x")})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if err := client.Send(bare); err != nil { // no ID
		t.Fatalf("send: %v", err)
	}
	marker, err := ipc.NewMessage(ipc.MsgPaneStatusReq, ipc.PaneStatusReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	marker.ID = "marker-1"
	if err := client.Send(marker); err != nil {
		t.Fatalf("send marker: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("the marker response never arrived")
		default:
		}
		resp, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if resp.Type == ipc.MsgPaneInputResp {
			t.Fatal("an id-less pane_input was answered — one critical frame per keystroke would force-disconnect the TUI")
		}
		if resp.Type == ipc.MsgPaneStatusResp && resp.ID == "marker-1" {
			return // the id-less input was dispatched before this and produced nothing
		}
	}
}
