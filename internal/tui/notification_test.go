package tui

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestNotificationCenter_AddEvent_Dedup(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "a", Title: "first"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "a", Title: "duplicate"})
	if nc.Count() != 1 {
		t.Errorf("Count: got %d, want 1 (dedup failed)", nc.Count())
	}
}

func TestNotificationCenter_AddEvent_NewestFirst(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "a", Title: "first"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "b", Title: "second"})
	if nc.events[0].ID != "b" {
		t.Errorf("events[0].ID: got %q, want %q", nc.events[0].ID, "b")
	}
}

func TestNotificationCenter_DismissSelected_CursorClamp(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "a"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "b"})
	nc.cursor = 1

	nc.DismissSelected()
	if nc.cursor != 0 {
		t.Errorf("cursor after dismiss last: got %d, want 0", nc.cursor)
	}
}

func TestNotificationCenter_DismissSelected_EmptyList(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "a"})
	nc.DismissSelected()
	if nc.Count() != 0 {
		t.Errorf("Count: got %d, want 0", nc.Count())
	}
	if nc.cursor != 0 {
		t.Errorf("cursor: got %d, want 0", nc.cursor)
	}
}

func TestNotificationCenter_HandleKey_Navigate(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.focused = true
	nc.AddEvent(ipc.PaneEventPayload{ID: "a", PaneID: "pane-1"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "b", PaneID: "pane-2"})

	// Cursor starts at 0; down moves to 1
	nc.HandleKey("down")
	if nc.cursor != 1 {
		t.Errorf("cursor after down: got %d, want 1", nc.cursor)
	}

	// Up moves back to 0
	nc.HandleKey("up")
	if nc.cursor != 0 {
		t.Errorf("cursor after up: got %d, want 0", nc.cursor)
	}

	// Enter on selected event returns navigate action
	action, _, paneID := nc.HandleKey("enter")
	if action != "navigate" {
		t.Errorf("action: got %q, want %q", action, "navigate")
	}
	if paneID != "pane-2" { // newest first, cursor 0 = "b"
		t.Errorf("paneID: got %q, want %q", paneID, "pane-2")
	}
}

func TestNotificationCenter_HandleKey_Dismiss(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "a"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "b"})

	action, eventID, _ := nc.HandleKey("d")
	if action != "dismiss" {
		t.Errorf("action: got %q, want %q", action, "dismiss")
	}
	if eventID != "b" { // newest first, cursor 0 = "b"
		t.Errorf("eventID: got %q, want %q", eventID, "b")
	}
	if nc.Count() != 1 {
		t.Errorf("Count: got %d, want 1", nc.Count())
	}
}

func TestNotificationCenter_HandleKey_DismissAll(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{ID: "a"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "b"})

	action, _, _ := nc.HandleKey("D")
	if action != "dismiss_all" {
		t.Errorf("action: got %q, want %q", action, "dismiss_all")
	}
	if nc.Count() != 0 {
		t.Errorf("Count: got %d, want 0", nc.Count())
	}
}

func TestNotificationCenter_HandleKey_Unfocus(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	action, _, _ := nc.HandleKey("esc")
	if action != "unfocus" {
		t.Errorf("action: got %q, want %q", action, "unfocus")
	}
}

func TestRelativeTime_Ranges(t *testing.T) {
	now := time.Now()
	tests := []struct {
		age      time.Duration
		expected string
	}{
		{3 * time.Second, "3s"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		got := relativeTime(now.Add(-tt.age))
		if got != tt.expected {
			t.Errorf("relativeTime(%v ago): got %q, want %q", tt.age, got, tt.expected)
		}
	}
}

func TestRelativeTime_Future_ReturnsNow(t *testing.T) {
	got := relativeTime(time.Now().Add(10 * time.Second))
	if got != "now" {
		t.Errorf("relativeTime(future): got %q, want %q", got, "now")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 3); got != "hel" {
		t.Errorf("truncateRunes: got %q, want %q", got, "hel")
	}
	if got := truncateRunes("hi", 10); got != "hi" {
		t.Errorf("truncateRunes no-op: got %q, want %q", got, "hi")
	}
	// Multi-byte: 日本語 is 3 runes
	if got := truncateRunes("日本語test", 3); got != "日本語" {
		t.Errorf("truncateRunes multi-byte: got %q, want %q", got, "日本語")
	}
}
