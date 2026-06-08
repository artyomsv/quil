package daemon

import (
	"strings"
	"testing"
	"time"
)

// TestToPaneEventPayload_MessageOverCapTruncatesAndMarks proves the wedge-
// prevention contract: any single event Message larger than the cap is
// truncated from the front, the trailing visible content is preserved, the
// payload size lands under the cap, and the Data map carries a "truncated"
// flag for consumers that want to know.
func TestToPaneEventPayload_MessageOverCapTruncatesAndMarks(t *testing.T) {
	// 8 KiB Message — double the cap.
	original := strings.Repeat("x", 4*1024) + "TAIL_VISIBLE"
	ev := PaneEvent{
		ID:        "evt-1",
		Title:     "Output idle",
		Message:   original,
		Severity:  "info",
		Timestamp: time.Unix(0, 0),
	}

	got := toPaneEventPayload(ev)

	if len(got.Message) > maxEventMessageBytes {
		t.Errorf("Message len after cap: got %d, want ≤ %d", len(got.Message), maxEventMessageBytes)
	}
	if !strings.HasPrefix(got.Message, truncationMarker) {
		t.Errorf("Message should be prefixed with truncation marker; got first 32 chars %q", got.Message[:32])
	}
	if !strings.HasSuffix(got.Message, "TAIL_VISIBLE") {
		t.Errorf("Message should preserve the tail (most recent content) after truncation; got suffix %q", got.Message[len(got.Message)-20:])
	}
	if got.Data["truncated"] != "1" {
		t.Errorf("Data[\"truncated\"] = %q, want \"1\"", got.Data["truncated"])
	}
}

// TestToPaneEventPayload_DataValueOverCapTruncates verifies the per-value cap
// catches misbehaving sources that stuff long strings into Data (e.g. a hook
// dumping a full prompt or a stack trace).
func TestToPaneEventPayload_DataValueOverCapTruncates(t *testing.T) {
	bigValue := strings.Repeat("y", 1024)
	ev := PaneEvent{
		ID:    "evt-1",
		Title: "tool_run",
		Data:  map[string]string{"args": bigValue, "short": "ok"},
	}

	got := toPaneEventPayload(ev)

	if len(got.Data["args"]) > maxEventDataValueBytes {
		t.Errorf("Data[args] len: got %d, want ≤ %d", len(got.Data["args"]), maxEventDataValueBytes)
	}
	if !strings.HasSuffix(got.Data["args"], truncationMarker) {
		t.Errorf("oversize Data value should be suffixed with truncation marker; got %q", got.Data["args"])
	}
	if got.Data["short"] != "ok" {
		t.Errorf("small Data values must pass through untouched; got %q", got.Data["short"])
	}
	if got.Data["truncated"] != "1" {
		t.Errorf("Data[truncated] = %q, want \"1\"", got.Data["truncated"])
	}
}

// TestToPaneEventPayload_WithinCapPassesThrough is the happy path — normal
// events must not be modified.
func TestToPaneEventPayload_WithinCapPassesThrough(t *testing.T) {
	ev := PaneEvent{
		ID:       "evt-1",
		PaneID:   "pane-1",
		Title:    "Process exited (code 0)",
		Message:  "build succeeded\n",
		Severity: "info",
		Data:     map[string]string{"exit_code": "0"},
	}

	got := toPaneEventPayload(ev)

	if got.Message != "build succeeded\n" {
		t.Errorf("Message mutated under cap; got %q", got.Message)
	}
	if got.Data["exit_code"] != "0" {
		t.Errorf("Data[exit_code] mutated; got %q", got.Data["exit_code"])
	}
	if _, ok := got.Data["truncated"]; ok {
		t.Errorf("under-cap event must not carry truncated marker; got Data=%v", got.Data)
	}
}

// TestToPaneEventPayload_NilDataNoTruncationMarker — when nothing is over the
// cap, we never allocate a Data map just to set the marker.
func TestToPaneEventPayload_NilDataNoTruncationMarker(t *testing.T) {
	ev := PaneEvent{ID: "evt-1", Title: "ok", Message: "short"}
	got := toPaneEventPayload(ev)
	if got.Data != nil {
		t.Errorf("expected nil Data when no truncation needed; got %v", got.Data)
	}
}
