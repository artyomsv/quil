package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestNotificationCenter_AddEvent_SameIDUpdatesInPlace mirrors the daemon's
// aggregation echo: a repeat (PaneID, Title) event reuses the prior event's
// ID and the sidebar must update the existing card in place AND bubble it
// to the front (so users see the bump and ×N badge change).
func TestNotificationCenter_AddEvent_SameIDUpdatesInPlace(t *testing.T) {
	t.Parallel()
	nc := NewNotificationCenter(40, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "shared", PaneID: "p1", Title: "Output idle", Message: "first"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "other", PaneID: "p2", Title: "Output idle", Message: "other"})
	nc.AddEvent(ipc.PaneEventPayload{
		ID:      "shared",
		PaneID:  "p1",
		Title:   "Output idle",
		Message: "second",
		Data:    map[string]string{"count": "2"},
	})

	if nc.Count() != 2 {
		t.Fatalf("count after update-in-place: got %d, want 2", nc.Count())
	}
	if nc.events[0].ID != "shared" {
		t.Errorf("aggregated card must bubble to position 0; got %q", nc.events[0].ID)
	}
	if nc.events[0].Message != "second" {
		t.Errorf("update-in-place must replace content; got %q, want %q", nc.events[0].Message, "second")
	}
	if nc.events[0].Data["count"] != "2" {
		t.Errorf("update must preserve count; got %q, want %q", nc.events[0].Data["count"], "2")
	}
}

func TestNotificationCenter_View_RendersCountBadge(t *testing.T) {
	t.Parallel()
	nc := NewNotificationCenter(40, 50)
	nc.visible = true
	nc.AddEvent(ipc.PaneEventPayload{
		ID:      "evt-1",
		PaneID:  "p1",
		Title:   "Waiting for input",
		Message: "claude prompt",
		Data:    map[string]string{"count": "7"},
	})

	rendered := nc.View(20)
	if !strings.Contains(rendered, "×7") {
		t.Errorf("View must render ×N badge when count > 1; output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Waiting for input") {
		t.Errorf("View must still render title; output:\n%s", rendered)
	}
}

func TestNotificationCenter_View_NoBadgeWhenCountOne(t *testing.T) {
	t.Parallel()
	nc := NewNotificationCenter(40, 50)
	nc.visible = true
	nc.AddEvent(ipc.PaneEventPayload{
		ID:    "evt-1",
		Title: "Output idle",
	})

	rendered := nc.View(20)
	if strings.Contains(rendered, "×") {
		t.Errorf("View must NOT render ×N for a single un-aggregated event; output:\n%s", rendered)
	}
}

// TestNotificationCenter_AddEvent_CursorFollowsLogicalEventOnAggregation —
// H3 invariant from the code review. When an event bumps to position 0 via
// the move-to-front aggregation path, a cursor that was pointing at a
// DIFFERENT event must follow that event's new index — never stay at the
// same numeric position (which would silently jump the user's selection
// onto a different card while they were reading it).
func TestNotificationCenter_AddEvent_CursorFollowsLogicalEventOnAggregation(t *testing.T) {
	t.Parallel()
	nc := NewNotificationCenter(40, 50)
	// Layout after seeding: [newest, middle, watched-card]. The user selects
	// watched-card at index 2 to read it.
	nc.AddEvent(ipc.PaneEventPayload{ID: "watched-card", PaneID: "p-watch", Title: "Output idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "middle", PaneID: "p-middle", Title: "Output idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "newest", PaneID: "p-new", Title: "Output idle"})
	nc.cursor = 2 // pointing at watched-card

	// Aggregate `middle` (was at index 1) — moves to position 0. Layout
	// becomes [middle, newest, watched-card]. Without the cursor-by-ID fix,
	// cursor=2 would still point at watched-card (lucky no-op here).
	nc.AddEvent(ipc.PaneEventPayload{ID: "middle", PaneID: "p-middle", Title: "Output idle",
		Data: map[string]string{"count": "2"}})

	if got := nc.events[nc.cursor].ID; got != "watched-card" {
		t.Errorf("after middle bump, cursor must point at watched-card; landed on %q (cursor=%d)", got, nc.cursor)
	}

	// Aggregate `watched-card` itself — it moves from index 2 to 0. The
	// cursor must follow because it WAS on watched-card.
	nc.AddEvent(ipc.PaneEventPayload{ID: "watched-card", PaneID: "p-watch", Title: "Output idle",
		Data: map[string]string{"count": "2"}})

	if got := nc.events[nc.cursor].ID; got != "watched-card" {
		t.Errorf("after watched-card bump, cursor must follow to position 0; landed on %q (cursor=%d)", got, nc.cursor)
	}
	if nc.cursor != 0 {
		t.Errorf("cursor should be at the aggregated event's new index (0); got %d", nc.cursor)
	}
}
