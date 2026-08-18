package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// Proves the dispatch arm is WIRED, not just that the handler works.
//
// This repo has shipped the opposite shape more than once: a handler covered by
// a direct-call test while the switch arm that reaches it was missing or pointed
// at the wrong slot, leaving the test green against unreachable code. So this
// drives a real client over a real socket and asserts the observable effect —
// that the daemon stops broadcasting pane output to the conn that asked to be
// excused, and keeps broadcasting it to one that did not.
func TestHandleSubscribe_DispatchArmStopsPaneOutputForThatClientOnly(t *testing.T) {
	d, sock := overlayServerDaemon(t)

	declined := attachTestClient(t, sock)
	defer declined.Close()
	subscribed := attachTestClient(t, sock)
	defer subscribed.Close()

	no := false
	sub, err := ipc.NewMessage(ipc.MsgSubscribe, ipc.SubscribePayload{PaneOutput: &no})
	if err != nil {
		t.Fatalf("build subscribe: %v", err)
	}
	if err := declined.Send(sub); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}

	// Drain the attach replay before probing: handleAttach answers with
	// workspace state, and reading that as "pane output arrived" would make
	// this pass for the wrong reason.
	drain := func(c *ipc.Client) {
		for {
			c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			if _, err := c.Receive(); err != nil {
				c.SetReadDeadline(time.Time{})
				return
			}
		}
	}
	drain(declined)
	drain(subscribed)

	out, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: "pane-probe", Data: []byte("live output"),
	})
	if err != nil {
		t.Fatalf("build pane output: %v", err)
	}

	// Poll rather than sleep: the subscribe frame and the broadcast are on
	// different goroutines, so a single shot could race the handler and pass
	// before the opt-out ever landed.
	deadline := time.Now().Add(3 * time.Second)
	for {
		d.broadcast(out)

		declined.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		_, err := declined.Receive()
		declined.SetReadDeadline(time.Time{})
		if err != nil {
			break // filtered — the dispatch arm ran and took effect
		}
		if time.Now().After(deadline) {
			t.Fatal("client still receives pane output after MsgSubscribe — the " +
				"case arm in handleMessage is not reached, or the handler does not " +
				"apply the opt-out to the arriving conn")
		}
	}

	// The other client must be untouched: the opt-out is per-connection, and a
	// filter that silenced everyone would satisfy the assertion above.
	d.broadcast(out)
	subscribed.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := subscribed.Receive()
	if err != nil {
		t.Fatalf("the client that did NOT opt out lost its pane output: %v", err)
	}
	if got.Type != ipc.MsgPaneOutput {
		t.Fatalf("got %q, want %q", got.Type, ipc.MsgPaneOutput)
	}
}
