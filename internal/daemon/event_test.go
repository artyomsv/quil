package daemon

import (
	"testing"
	"time"
)

func TestEventQueue_Push_BoundsToMax(t *testing.T) {
	q := newEventQueue(3)
	for i := 0; i < 5; i++ {
		q.Push(PaneEvent{ID: string(rune('a' + i)), Title: "event"})
	}
	if q.Count() != 3 {
		t.Errorf("Count: got %d, want 3", q.Count())
	}
	events := q.Events()
	// Newest first
	if events[0].ID != "e" {
		t.Errorf("events[0].ID: got %q, want %q", events[0].ID, "e")
	}
}

func TestEventQueue_Push_NewestFirst(t *testing.T) {
	q := newEventQueue(10)
	q.Push(PaneEvent{ID: "first"})
	q.Push(PaneEvent{ID: "second"})
	events := q.Events()
	if events[0].ID != "second" {
		t.Errorf("events[0].ID: got %q, want %q", events[0].ID, "second")
	}
}

func TestEventQueue_Dismiss(t *testing.T) {
	q := newEventQueue(10)
	q.Push(PaneEvent{ID: "a"})
	q.Push(PaneEvent{ID: "b"})
	q.Dismiss("a")
	if q.Count() != 1 {
		t.Errorf("Count after dismiss: got %d, want 1", q.Count())
	}
	if q.Events()[0].ID != "b" {
		t.Errorf("remaining event: got %q, want %q", q.Events()[0].ID, "b")
	}
}

func TestEventQueue_DismissAll(t *testing.T) {
	q := newEventQueue(10)
	q.Push(PaneEvent{ID: "a"})
	q.Push(PaneEvent{ID: "b"})
	q.DismissAll()
	if q.Count() != 0 {
		t.Errorf("Count after dismiss all: got %d, want 0", q.Count())
	}
}

func TestEventQueue_WatcherWoken(t *testing.T) {
	q := newEventQueue(10)
	ch := make(chan *PaneEvent, 1)
	w := &connWatcher{paneIDs: nil, ch: ch} // nil = all panes
	q.AddWatcher(w)

	q.Push(PaneEvent{ID: "evt1", PaneID: "pane-1"})

	select {
	case evt := <-ch:
		if evt.ID != "evt1" {
			t.Errorf("watcher got %q, want %q", evt.ID, "evt1")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher not woken within 1s")
	}
}

func TestEventQueue_WatcherFiltered(t *testing.T) {
	q := newEventQueue(10)
	ch := make(chan *PaneEvent, 1)
	w := &connWatcher{
		paneIDs: map[string]bool{"pane-2": true},
		ch:      ch,
	}
	q.AddWatcher(w)

	// Push event for pane-1 (should NOT wake watcher)
	q.Push(PaneEvent{ID: "evt1", PaneID: "pane-1"})
	select {
	case <-ch:
		t.Fatal("watcher should not be woken for pane-1")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	// Push event for pane-2 (should wake watcher)
	q.Push(PaneEvent{ID: "evt2", PaneID: "pane-2"})
	select {
	case evt := <-ch:
		if evt.ID != "evt2" {
			t.Errorf("watcher got %q, want %q", evt.ID, "evt2")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher not woken for pane-2")
	}
}

func TestEventQueue_RemoveWatcher(t *testing.T) {
	q := newEventQueue(10)
	ch := make(chan *PaneEvent, 1)
	w := &connWatcher{paneIDs: nil, ch: ch}
	q.AddWatcher(w)
	q.RemoveWatcher(w)

	q.Push(PaneEvent{ID: "evt1"})
	select {
	case <-ch:
		t.Fatal("removed watcher should not be woken")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}
