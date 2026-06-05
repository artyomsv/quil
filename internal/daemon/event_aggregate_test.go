package daemon

import (
	"testing"
	"time"
)

// TestEventQueue_Push_AggregatesSameTitleSamePane proves the field-observed
// "two pane-a39ad0c Waiting for input cards" issue is now collapsed into one
// card with a count badge.
func TestEventQueue_Push_AggregatesSameTitleSamePane(t *testing.T) {
	q := newEventQueue(10)
	t0 := time.Unix(0, 0).Add(1 * time.Second)
	q.Push(PaneEvent{ID: "first-id", PaneID: "p1", Title: "Waiting for input", Timestamp: t0})
	q.Push(PaneEvent{ID: "second-id", PaneID: "p1", Title: "Waiting for input", Timestamp: t0.Add(30 * time.Second)})

	events := q.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 aggregated entry, got %d", len(events))
	}
	got := events[0]
	if got.ID != "first-id" {
		t.Errorf("aggregation must reuse the older ID so the TUI updates in place; got %q, want %q", got.ID, "first-id")
	}
	if got.Data["count"] != "2" {
		t.Errorf("count after one aggregation: got %q, want %q", got.Data["count"], "2")
	}
	if !got.Timestamp.Equal(t0.Add(30 * time.Second)) {
		t.Errorf("aggregated timestamp should be the newer one; got %v, want %v", got.Timestamp, t0.Add(30*time.Second))
	}
}

func TestEventQueue_Push_AggregationKeepsCountingAcrossManyHits(t *testing.T) {
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(1 * time.Second)
	for i := 0; i < 5; i++ {
		q.Push(PaneEvent{
			ID:        "evt-" + string(rune('a'+i)),
			PaneID:    "p1",
			Title:     "Output idle",
			Timestamp: at.Add(time.Duration(i) * time.Second),
		})
	}
	events := q.Events()
	if len(events) != 1 {
		t.Fatalf("five same-pane same-title pushes must collapse to one entry; got %d", len(events))
	}
	if events[0].Data["count"] != "5" {
		t.Errorf("count after five aggregations: got %q, want %q", events[0].Data["count"], "5")
	}
}

func TestEventQueue_Push_DifferentTitleNotAggregated(t *testing.T) {
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(1 * time.Second)
	q.Push(PaneEvent{ID: "a", PaneID: "p1", Title: "Output idle", Timestamp: at})
	q.Push(PaneEvent{ID: "b", PaneID: "p1", Title: "Process exited (code 0)", Timestamp: at.Add(time.Second)})

	if got := q.Count(); got != 2 {
		t.Errorf("different titles must remain distinct; queue=%d, want 2", got)
	}
}

func TestEventQueue_Push_DifferentPaneNotAggregated(t *testing.T) {
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(1 * time.Second)
	q.Push(PaneEvent{ID: "a", PaneID: "p1", Title: "Output idle", Timestamp: at})
	q.Push(PaneEvent{ID: "b", PaneID: "p2", Title: "Output idle", Timestamp: at.Add(time.Second)})

	if got := q.Count(); got != 2 {
		t.Errorf("different panes must remain distinct; queue=%d, want 2", got)
	}
}

func TestEventQueue_Push_EmptyPaneIDNeverAggregates(t *testing.T) {
	// Daemon-level events without a pane source must remain distinct so
	// they're never accidentally collapsed.
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(1 * time.Second)
	q.Push(PaneEvent{ID: "a", PaneID: "", Title: "ping", Timestamp: at})
	q.Push(PaneEvent{ID: "b", PaneID: "", Title: "ping", Timestamp: at.Add(time.Second)})

	if got := q.Count(); got != 2 {
		t.Errorf("paneless events must not aggregate; queue=%d, want 2", got)
	}
}

func TestEventQueue_Push_AggregationMovesToFront(t *testing.T) {
	// Insert: A(p1), B(p2), then A again with same (p1, Output idle). The
	// repeat must end up at position 0, not at its old position.
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(1 * time.Second)
	q.Push(PaneEvent{ID: "a1", PaneID: "p1", Title: "Output idle", Timestamp: at})
	q.Push(PaneEvent{ID: "b1", PaneID: "p2", Title: "Output idle", Timestamp: at.Add(time.Second)})
	q.Push(PaneEvent{ID: "a2", PaneID: "p1", Title: "Output idle", Timestamp: at.Add(2 * time.Second)})

	events := q.Events()
	if len(events) != 2 {
		t.Fatalf("a1+a2 collapses; queue should hold {a-aggregated, b}; got %d entries", len(events))
	}
	// Newest first: aggregated p1 first, then p2.
	if events[0].PaneID != "p1" {
		t.Errorf("aggregated event should bubble to position 0; got pane %q", events[0].PaneID)
	}
	if events[0].Data["count"] != "2" {
		t.Errorf("aggregated count: got %q, want %q", events[0].Data["count"], "2")
	}
}
