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
