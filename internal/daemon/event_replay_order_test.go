package daemon

import (
	"testing"
	"time"
)

// The event queue stores newest-first, because that is what a reader browsing
// notifications wants. The attach replay is not a reader: the TUI rebuilds
// each pane's work state by applying these events as ordered transitions, so
// iterating the stored order plays a pane's history backwards and lands on the
// state implied by its OLDEST event.
//
// Reported 2026-08-03: three claude panes parked on "Claude is waiting for
// your input" came back from a TUI restart with spinners running and nothing
// happening behind them. Their real last event was the park; the replay
// applied it FIRST and the turn's start LAST.
func TestEventsOldestFirst_ReplaysAPaneHistoryForwards(t *testing.T) {
	q := newEventQueue(50)

	// The exact sequence logged for pane-fa75ba78, in the order it happened.
	base := time.Date(2026, 8, 3, 0, 44, 0, 0, time.UTC)
	history := []struct {
		title  string
		offset time.Duration
	}{
		{"Resumed after AskUserQuestion", 36 * time.Second},
		{"Reply ready", 42 * time.Second},
		{"Claude is waiting for your input", 102 * time.Second},
	}
	for _, h := range history {
		q.Push(PaneEvent{
			ID:        h.title,
			PaneID:    "pane-fa75ba78",
			Title:     h.title,
			Timestamp: base.Add(h.offset),
		})
	}

	got := q.EventsOldestFirst()
	if len(got) != len(history) {
		t.Fatalf("replayed %d events, want %d", len(got), len(history))
	}
	for i, h := range history {
		if got[i].Title != h.title {
			t.Errorf("replay position %d = %q, want %q", i, got[i].Title, h.title)
		}
	}

	// The property that actually matters: the LAST event replayed is the last
	// event that happened, so a state machine fed this sequence ends where the
	// pane really is.
	last := got[len(got)-1]
	if last.Title != "Claude is waiting for your input" {
		t.Errorf("replay ends on %q, want the park — a pane rebuilt from this "+
			"sequence would show the wrong state", last.Title)
	}

	// Events() keeps its own contract: newest first, for the readers.
	if newest := q.Events()[0]; newest.Title != "Claude is waiting for your input" {
		t.Errorf("Events()[0] = %q, want the newest event", newest.Title)
	}
}

// Aggregation moves a repeated (PaneID, Title) event to the front of the
// queue, so the surviving entries stay ordered by their EFFECTIVE timestamps —
// reversing them is still chronological. Pinned because the collapse is what
// makes "reverse the slice" defensible rather than merely convenient.
func TestEventsOldestFirst_SurvivesAggregation(t *testing.T) {
	q := newEventQueue(50)
	base := time.Date(2026, 8, 3, 0, 44, 0, 0, time.UTC)

	q.Push(PaneEvent{ID: "a", PaneID: "p1", Title: "Reply ready", Timestamp: base})
	q.Push(PaneEvent{ID: "b", PaneID: "p1", Title: "Claude is waiting for your input", Timestamp: base.Add(time.Second)})
	// Second "Reply ready" collapses onto the first and moves to the front.
	q.Push(PaneEvent{ID: "c", PaneID: "p1", Title: "Reply ready", Timestamp: base.Add(2 * time.Second)})

	got := q.EventsOldestFirst()
	if len(got) != 2 {
		t.Fatalf("queue holds %d events after aggregation, want 2", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("position %d (%s) predates position %d (%s) — replay is out of order",
				i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
	if last := got[len(got)-1]; last.Title != "Reply ready" {
		t.Errorf("replay ends on %q, want the aggregated newest event", last.Title)
	}
}

// EventsOldestFirst must not alias or disturb the stored slice — the queue is
// read by the attach goroutine while hook events keep arriving.
func TestEventsOldestFirst_IsACopy(t *testing.T) {
	q := newEventQueue(50)
	q.Push(PaneEvent{ID: "a", PaneID: "p1", Title: "one"})
	q.Push(PaneEvent{ID: "b", PaneID: "p1", Title: "two"})

	got := q.EventsOldestFirst()
	got[0].Title = "mutated"

	if stored := q.Events(); stored[len(stored)-1].Title != "one" {
		t.Errorf("mutating the returned slice changed the queue: %q", stored[len(stored)-1].Title)
	}
}
