package ipc_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// End-to-end proof that opting out actually removes frames from the wire.
//
// The predicate tests in subscribe_test.go check wantsFrame in isolation, and
// that is not the same claim: Broadcast could stop consulting it and every one
// of them would stay green. This drives a real Server with two real clients and
// asserts what each socket receives, which is the only thing the optimisation
// is for.
func TestBroadcast_SkipsPaneOutputForOptedOutConnOnly(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "subscribe.sock")

	// The handler stands in for the daemon's: a client asks to be excused and
	// the server records it on that client's own conn.
	srv := ipc.NewServer(sockPath, func(c *ipc.Conn, m *ipc.Message) {
		if m.Type == ipc.MsgSubscribe {
			var p ipc.SubscribePayload
			if err := m.DecodePayload(&p); err == nil && p.PaneOutput != nil {
				c.SetPaneOutputWanted(*p.PaneOutput)
			}
		}
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	subscribed, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("subscribed client connect: %v", err)
	}
	defer subscribed.Close()

	declined, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("declining client connect: %v", err)
	}
	defer declined.Close()

	waitForConnCount(t, srv, 2, 2*time.Second)

	no := false
	sub, err := ipc.NewMessage(ipc.MsgSubscribe, ipc.SubscribePayload{PaneOutput: &no})
	if err != nil {
		t.Fatalf("build subscribe: %v", err)
	}
	if err := declined.Send(sub); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}

	// The opt-out must be recorded before the broadcast, or this races into a
	// false pass. Poll the observable effect rather than sleeping.
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "pane-probe", Data: []byte("x")})
		if err != nil {
			t.Fatalf("build probe: %v", err)
		}
		srv.Broadcast(out)

		declined.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		if _, err := declined.Receive(); err != nil {
			break // no pane output delivered — the opt-out has landed
		}
		if time.Now().After(deadline) {
			t.Fatal("declining client still receives pane output after opting out")
		}
	}
	declined.SetReadDeadline(time.Time{})

	// The subscribed client must still get pane output...
	payload := ipc.PaneOutputPayload{PaneID: "pane-1", Data: []byte("hello")}
	out, err := ipc.NewMessage(ipc.MsgPaneOutput, payload)
	if err != nil {
		t.Fatalf("build pane output: %v", err)
	}
	srv.Broadcast(out)

	subscribed.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := subscribed.Receive()
	if err != nil {
		t.Fatalf("subscribed client got no pane output: %v", err)
	}
	if got.Type != ipc.MsgPaneOutput {
		t.Fatalf("subscribed client got %q, want %q", got.Type, ipc.MsgPaneOutput)
	}

	// ...and the declining client must still get everything else, or the filter
	// is silencing more than it was asked to.
	state, err := ipc.NewMessage(ipc.MsgWorkspaceState, map[string]any{"tabs": []any{}})
	if err != nil {
		t.Fatalf("build workspace state: %v", err)
	}
	srv.Broadcast(state)

	declined.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err = declined.Receive()
	if err != nil {
		t.Fatalf("declining client lost must-deliver traffic: %v", err)
	}
	if got.Type != ipc.MsgWorkspaceState {
		t.Fatalf("declining client got %q, want %q — only pane output may be filtered",
			got.Type, ipc.MsgWorkspaceState)
	}
}

// TestBroadcast_SkipsPaneOutputForHeldConnOnly is the SetHoldPaneOutput analog
// of TestBroadcast_SkipsPaneOutputForOptedOutConnOnly above: the daemon holds
// a newly-attaching client off live pane_output (to deliver its replay without
// interleaving), and that hold must be as real on the wire as the client's own
// MsgSubscribe opt-out — filtering pane_output only, never must-deliver
// traffic, and never touching any other conn.
//
// Unlike the opt-out, holding is daemon-internal bookkeeping with no wire
// message of its own (a later task wires the daemon side), so this drives it
// directly through the *Conn the server hands back via ConnsSnapshot rather
// than through a client-sent message.
func TestBroadcast_SkipsPaneOutputForHeldConnOnly(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "hold.sock")

	srv := ipc.NewServer(sockPath, func(c *ipc.Conn, m *ipc.Message) {}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	unheld, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("unheld client connect: %v", err)
	}
	defer unheld.Close()
	waitForConnCount(t, srv, 1, 2*time.Second)

	held, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("held client connect: %v", err)
	}
	defer held.Close()
	waitForConnCount(t, srv, 2, 2*time.Second)

	conns := srv.ConnsSnapshot()
	if len(conns) != 2 {
		t.Fatalf("ConnsSnapshot: got %d conns, want 2", len(conns))
	}
	// Accept order matches dial order: the first entry is unheld's conn, the
	// second is held's — hold the second one.
	conns[1].SetHoldPaneOutput(true)

	// The held client must receive no pane output while held.
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "pane-probe", Data: []byte("x")})
		if err != nil {
			t.Fatalf("build probe: %v", err)
		}
		srv.Broadcast(out)

		held.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		if _, err := held.Receive(); err != nil {
			break // no pane output delivered — the hold has landed
		}
		if time.Now().After(deadline) {
			t.Fatal("held client still receives pane output while held")
		}
	}
	held.SetReadDeadline(time.Time{})

	// The unheld client must still get pane output...
	payload := ipc.PaneOutputPayload{PaneID: "pane-1", Data: []byte("hello")}
	out, err := ipc.NewMessage(ipc.MsgPaneOutput, payload)
	if err != nil {
		t.Fatalf("build pane output: %v", err)
	}
	srv.Broadcast(out)

	unheld.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := unheld.Receive()
	if err != nil {
		t.Fatalf("unheld client got no pane output: %v", err)
	}
	if got.Type != ipc.MsgPaneOutput {
		t.Fatalf("unheld client got %q, want %q", got.Type, ipc.MsgPaneOutput)
	}

	// ...and the held client must still get everything else, or the hold is
	// silencing more than must-deliver traffic should allow.
	state, err := ipc.NewMessage(ipc.MsgWorkspaceState, map[string]any{"tabs": []any{}})
	if err != nil {
		t.Fatalf("build workspace state: %v", err)
	}
	srv.Broadcast(state)

	held.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err = held.Receive()
	if err != nil {
		t.Fatalf("held client lost must-deliver traffic: %v", err)
	}
	if got.Type != ipc.MsgWorkspaceState {
		t.Fatalf("held client got %q, want %q — only pane output may be filtered",
			got.Type, ipc.MsgWorkspaceState)
	}

	// Releasing the hold must restore the live stream.
	conns[1].SetHoldPaneOutput(false)
	out2, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: "pane-2", Data: []byte("world")})
	if err != nil {
		t.Fatalf("build pane output: %v", err)
	}
	srv.Broadcast(out2)

	held.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err = held.Receive()
	if err != nil {
		t.Fatalf("released client got no pane output: %v", err)
	}
	if got.Type != ipc.MsgPaneOutput {
		t.Fatalf("released client got %q, want %q", got.Type, ipc.MsgPaneOutput)
	}
}
