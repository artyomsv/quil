package tui

import (
	"testing"
)

// TestUpdate_WorkStateOnlyEventPostsNoSidebarCard drives the real call site
// rather than the predicate.
//
// TestWorkStateOnlyEvent already covers workStateOnlyEvent itself, and that is
// exactly the gap: a passing predicate test says nothing about whether Update
// consults it. Reverting model.go's paneEventMsg arm to the pre-change
// `msg.Type == "hook.claude.PostToolUse"` leaves the entire package green
// otherwise — the recorded "unit test bypassing the call site" shape, where the
// decision function and its mutation both pass against wiring nothing reaches.
//
// The daemon-side fix does NOT make this redundant. emitEvent deliberately
// still BROADCASTS work-state-only events (it is the only route by which the
// spinner learns a pane is working) and Update's paneEventMsg arm calls
// AddEvent on the broadcast — so a regression here posts a "Working" card to
// the live sidebar every workHeartbeatInterval on every working pane. It just
// no longer survives a reattach replay.
func TestUpdate_WorkStateOnlyEventPostsNoSidebarCard(t *testing.T) {
	m := modelForWorkTest()

	for _, evt := range []struct{ id, typ, title string }{
		{"evt-1", "hook.claude.PreToolUse", "Working"},
		{"evt-2", "hook.claude.PostToolUse", "Resumed after AskUserQuestion"},
	} {
		updated, _ := m.Update(paneEventMsg{ID: evt.id, PaneID: "p1", Type: evt.typ, Title: evt.title})
		m = updated.(Model)
		if n := m.notifications.Count(); n != 0 {
			t.Fatalf("%s must not post a sidebar card: got %d", evt.typ, n)
		}
	}

	// Control: without this, deleting the AddEvent call entirely would satisfy
	// every assertion above. A finished turn is exactly what the sidebar is for.
	updated, _ := m.Update(paneEventMsg{ID: "evt-3", PaneID: "p1", Type: "hook.claude.Stop", Title: "Reply ready"})
	m = updated.(Model)
	if n := m.notifications.Count(); n != 1 {
		t.Errorf("a finished turn must still post a card: got %d, want 1", n)
	}
}
