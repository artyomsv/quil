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
