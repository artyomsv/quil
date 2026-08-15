package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// awaitPaneEvent drains the client's frames until a MsgPaneEvent of the given
// type arrives, or the deadline passes. Other frame types (workspace state,
// pane output) are skipped rather than failed on — the daemon is free to send
// them and the test is only interested in whether this event got through.
func awaitPaneEvent(t *testing.T, c *ipc.Client, eventType string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.SetReadDeadline(deadline); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		msg, err := c.Receive()
		if err != nil {
			return false
		}
		if msg.Type != ipc.MsgPaneEvent {
			continue
		}
		var p ipc.PaneEventPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane event: %v", err)
		}
		if p.Type == eventType {
			return true
		}
	}
	return false
}

// TestEmitEvent_WorkStateOnlyEventBroadcastsWithoutQueueing pins BOTH halves of
// the work-state-only contract, and both are load-bearing in opposite
// directions.
//
// It must NOT be queued. The queue is bounded (50) and eventQueue.Push
// aggregates by (PaneID, Title) and then re-prepends the aggregated entry — so
// a heartbeat carrying the constant title "Working" holds a slot per working
// pane AND jumps ahead of every older event each time it fires. On a workspace
// of AI panes that displaces genuine notifications out of the attach-replay
// window, and a missed permission prompt is the one this project treats as
// silent and terminal. It also woke every watch_notifications watcher on the
// pane, turning the tool documented as "replaces polling" back into a poll.
//
// It MUST still be broadcast. Keeping it out of the queue leaves the live
// broadcast as the ONLY route by which a client learns the pane is working, so
// a "fix" that returns before d.broadcast would silence the spinner outright —
// the exact bug the heartbeat exists to fix. Hence a real IPC server and a real
// client here rather than a queue-count assertion: a count-only test passes
// just as happily with the broadcast deleted.
func TestEmitEvent_WorkStateOnlyEventBroadcastsWithoutQueueing(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	d.session.RestoreTab(
		&Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-busy"}},
		[]*Pane{{ID: "pane-busy", TabID: "tab-1", Type: "terminal"}},
	)

	c := attachTestClient(t, sock)
	defer c.Close()
	waitUntil(t, "the client to be accepted", func() bool { return d.server.ConnCount() == 1 })

	d.emitEvent(PaneEvent{ID: "evt-1", PaneID: "pane-busy", Type: "hook.claude.PreToolUse", Title: "Working"})

	if !awaitPaneEvent(t, c, "hook.claude.PreToolUse") {
		t.Error("the heartbeat must still reach live clients — it is the only thing that lights the spinner")
	}
	if n := d.events.Count(); n != 0 {
		t.Errorf("work-state-only events must not enter the notification queue: got %d", n)
	}
}

// TestEmitEvent_TurnEndingEventsStillQueue is the control that stops the
// suppression from being widened into "hook events do not queue". Every other
// work-state edge says something a user acts on — a finished turn, a permission
// prompt, a named subagent completing — and those are what the sidebar and the
// attach replay exist for. Only the two heartbeats are silent.
func TestEmitEvent_TurnEndingEventsStillQueue(t *testing.T) {
	d := newTestDaemon(t)
	d.session.RestoreTab(
		&Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-busy"}},
		[]*Pane{{ID: "pane-busy", TabID: "tab-1", Type: "terminal"}},
	)

	d.emitEvent(PaneEvent{ID: "evt-1", PaneID: "pane-busy", Type: "hook.claude.Stop", Title: "Reply ready"})
	d.emitEvent(PaneEvent{ID: "evt-2", PaneID: "pane-busy", Type: "hook.claude.StopFailure", Title: "Turn failed: API Error: 500"})
	d.emitEvent(PaneEvent{ID: "evt-3", PaneID: "pane-busy", Type: "hook.claude.PermissionRequest", Title: "Needs approval: Bash"})
	d.emitEvent(PaneEvent{ID: "evt-4", PaneID: "pane-busy", Type: "hook.claude.SubagentStop", Title: "spec-reviewer done"})

	if n := d.events.Count(); n != 4 {
		t.Errorf("user-facing hook events must still queue: got %d, want 4", n)
	}
}
