package daemon

import (
	"testing"
	"time"
)

// TestEventQueue_FindSince_ReturnsOldestNewerEvent demonstrates the contract
// that watch_notifications uses: when an agent passes since_timestamp, the
// daemon walks the queue oldest-to-newest and returns the first event whose
// timestamp is strictly greater than the marker. Oldest-first matters
// because agents handle events in order — jumping straight to the newest
// would skip intermediate state changes.
func TestEventQueue_FindSince_ReturnsOldestNewerEvent(t *testing.T) {
	q := newEventQueue(10)
	t0 := time.Unix(0, 0).Add(1000 * time.Millisecond)
	q.Push(PaneEvent{ID: "old", PaneID: "p1", Timestamp: t0})
	q.Push(PaneEvent{ID: "mid", PaneID: "p1", Timestamp: t0.Add(50 * time.Millisecond)})
	q.Push(PaneEvent{ID: "new", PaneID: "p1", Timestamp: t0.Add(100 * time.Millisecond)})

	got := q.FindSince(t0.UnixMilli(), nil)
	if got == nil {
		t.Fatal("FindSince returned nil; want the 'mid' event")
	}
	if got.ID != "mid" {
		t.Errorf("FindSince: got %q, want %q (the OLDEST event newer than the marker)", got.ID, "mid")
	}
}

func TestEventQueue_FindSince_ExclusiveOnTimestamp(t *testing.T) {
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(2000 * time.Millisecond)
	q.Push(PaneEvent{ID: "exact-match", PaneID: "p1", Timestamp: at})

	got := q.FindSince(at.UnixMilli(), nil)
	if got != nil {
		t.Errorf("event with timestamp == marker must NOT be returned (strict inequality); got %q", got.ID)
	}
}

func TestEventQueue_FindSince_RespectsPaneFilter(t *testing.T) {
	q := newEventQueue(10)
	t0 := time.Unix(0, 0).Add(1000 * time.Millisecond)
	q.Push(PaneEvent{ID: "a", PaneID: "pane-A", Timestamp: t0.Add(10 * time.Millisecond)})
	q.Push(PaneEvent{ID: "b", PaneID: "pane-B", Timestamp: t0.Add(20 * time.Millisecond)})
	q.Push(PaneEvent{ID: "c", PaneID: "pane-A", Timestamp: t0.Add(30 * time.Millisecond)})

	filter := map[string]bool{"pane-A": true}
	got := q.FindSince(t0.UnixMilli(), filter)
	if got == nil {
		t.Fatal("FindSince should have matched pane-A")
	}
	if got.ID != "a" {
		t.Errorf("FindSince filter: got %q, want %q (oldest matching pane-A)", got.ID, "a")
	}
}

func TestEventQueue_FindSince_NoMatchReturnsNil(t *testing.T) {
	q := newEventQueue(10)
	at := time.Unix(0, 0).Add(500 * time.Millisecond)
	q.Push(PaneEvent{ID: "old", PaneID: "p1", Timestamp: at})

	got := q.FindSince(at.Add(1*time.Hour).UnixMilli(), nil)
	if got != nil {
		t.Errorf("no event newer than far-future marker: got %q, want nil", got.ID)
	}
}

func TestEventQueue_FindSince_EmptyQueue(t *testing.T) {
	q := newEventQueue(10)
	if got := q.FindSince(0, nil); got != nil {
		t.Errorf("empty queue: got %q, want nil", got.ID)
	}
}
