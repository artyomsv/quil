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
// flag (under the reserved _quil_ namespace) for consumers that want to know.
func TestToPaneEventPayload_MessageOverCapTruncatesAndMarks(t *testing.T) {
	t.Parallel()
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
	if got.Data[truncatedFlagKey] != "1" {
		t.Errorf("Data[%q] = %q, want \"1\"", truncatedFlagKey, got.Data[truncatedFlagKey])
	}
}

// TestToPaneEventPayload_DataValueOverCapTruncates verifies the per-value cap
// catches misbehaving sources that stuff long strings into Data (e.g. a hook
// dumping a full prompt or a stack trace).
func TestToPaneEventPayload_DataValueOverCapTruncates(t *testing.T) {
	t.Parallel()
	// 4 KiB — guaranteed to exceed the 1 KiB Data value cap regardless of
	// future tuning. The exact-boundary test below pins the cap edge.
	bigValue := strings.Repeat("y", 4*1024)
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
	if got.Data[truncatedFlagKey] != "1" {
		t.Errorf("Data[%q] = %q, want \"1\"", truncatedFlagKey, got.Data[truncatedFlagKey])
	}
}

// TestToPaneEventPayload_WithinCapPassesThrough is the happy path — normal
// events must not be modified.
func TestToPaneEventPayload_WithinCapPassesThrough(t *testing.T) {
	t.Parallel()
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
	if _, ok := got.Data[truncatedFlagKey]; ok {
		t.Errorf("under-cap event must not carry truncated marker; got Data=%v", got.Data)
	}
}

// TestToPaneEventPayload_NilDataNoTruncationMarker — when nothing is over the
// cap, we never allocate a Data map just to set the marker.
func TestToPaneEventPayload_NilDataNoTruncationMarker(t *testing.T) {
	t.Parallel()
	ev := PaneEvent{ID: "evt-1", Title: "ok", Message: "short"}
	got := toPaneEventPayload(ev)
	if got.Data != nil {
		t.Errorf("expected nil Data when no truncation needed; got %v", got.Data)
	}
}

// TestToPaneEventPayload_ExactCapBoundary guards against off-by-one in the
// `> cap` condition. Messages and Data values at *exactly* the cap must pass
// through untouched; one byte over must trigger truncation. A future refactor
// to `>=` (a tempting cleanup) would silently break this.
func TestToPaneEventPayload_ExactCapBoundary(t *testing.T) {
	t.Parallel()

	exactMessage := strings.Repeat("m", maxEventMessageBytes)
	exactValue := strings.Repeat("v", maxEventDataValueBytes)
	ev := PaneEvent{
		ID:      "evt-1",
		Title:   "exact",
		Message: exactMessage,
		Data:    map[string]string{"v": exactValue},
	}
	got := toPaneEventPayload(ev)

	if got.Message != exactMessage {
		t.Errorf("exact-cap Message must pass through; got len=%d, want %d", len(got.Message), len(exactMessage))
	}
	if got.Data["v"] != exactValue {
		t.Errorf("exact-cap Data value must pass through; got len=%d, want %d", len(got.Data["v"]), len(exactValue))
	}
	if _, ok := got.Data[truncatedFlagKey]; ok {
		t.Errorf("exact-cap event must not carry truncated marker; got Data=%v", got.Data)
	}

	// One byte over → truncates.
	overMessage := exactMessage + "X"
	overValue := exactValue + "X"
	ev2 := PaneEvent{
		ID:      "evt-2",
		Title:   "over",
		Message: overMessage,
		Data:    map[string]string{"v": overValue},
	}
	got2 := toPaneEventPayload(ev2)
	if !strings.HasPrefix(got2.Message, truncationMarker) {
		t.Errorf("len(cap)+1 Message must trigger truncation; got first 32 chars %q", got2.Message[:32])
	}
	if !strings.HasSuffix(got2.Data["v"], truncationMarker) {
		t.Errorf("len(cap)+1 Data value must trigger truncation; got %q", got2.Data["v"])
	}
}

// TestToPaneEventPayload_BothOverCapSetsFlagOnce — when Message AND a Data
// value are both over cap, the truncated flag is set exactly once (the map
// write is idempotent) and both fields are truncated.
func TestToPaneEventPayload_BothOverCapSetsFlagOnce(t *testing.T) {
	t.Parallel()
	ev := PaneEvent{
		ID:      "evt-1",
		Title:   "both",
		Message: strings.Repeat("m", maxEventMessageBytes+10),
		Data:    map[string]string{"args": strings.Repeat("v", maxEventDataValueBytes+10)},
	}
	got := toPaneEventPayload(ev)

	if !strings.HasPrefix(got.Message, truncationMarker) {
		t.Errorf("Message must be truncated; got first 32 chars %q", got.Message[:32])
	}
	if !strings.HasSuffix(got.Data["args"], truncationMarker) {
		t.Errorf("Data[args] must be truncated; got %q", got.Data["args"])
	}
	if got.Data[truncatedFlagKey] != "1" {
		t.Errorf("flag must be set; got Data[%q] = %q", truncatedFlagKey, got.Data[truncatedFlagKey])
	}
}

// TestToPaneEventPayload_ReservedKeyDoesNotClobberCaller — emitters can use
// their own keys without fear of collision; the reserved _quil_ prefix
// guarantees daemon-internal flags never overwrite caller data.
func TestToPaneEventPayload_ReservedKeyDoesNotClobberCaller(t *testing.T) {
	t.Parallel()
	ev := PaneEvent{
		ID:    "evt-1",
		Title: "with truncated user key",
		Data: map[string]string{
			"truncated": "user-supplied-not-clobbered",
			"args":      strings.Repeat("v", maxEventDataValueBytes+10), // triggers cap
		},
	}
	got := toPaneEventPayload(ev)

	if got.Data["truncated"] != "user-supplied-not-clobbered" {
		t.Errorf("caller's \"truncated\" key must survive; got %q", got.Data["truncated"])
	}
	if got.Data[truncatedFlagKey] != "1" {
		t.Errorf("daemon flag must be set under reserved key; got %q", got.Data[truncatedFlagKey])
	}
}
