package hookevents

import (
	"sync"
	"testing"
	"time"
)

// emitRecorder is a thread-safe sink the Ingester emits into during tests.
type emitRecorder struct {
	mu      sync.Mutex
	events  []Payload
}

func (r *emitRecorder) emit(p Payload) {
	r.mu.Lock()
	r.events = append(r.events, p)
	r.mu.Unlock()
}

func (r *emitRecorder) drain() []Payload {
	r.mu.Lock()
	out := append([]Payload(nil), r.events...)
	r.events = nil
	r.mu.Unlock()
	return out
}

func basePayload(seq uint64) Payload {
	return Payload{
		V:         SchemaVersion,
		PaneID:    "pane-1",
		Source:    SourceClaude,
		HookEvent: "PermissionRequest",
		Title:     "Needs approval: Bash",
		Severity:  SeverityWarning,
		TsMs:      int64(seq),
		Seq:       seq,
	}
}

func TestIngester_Submit_CoalescesBurst(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	// 5 rapid submissions of the same (paneID, hook_event). Should collapse
	// to 1 emit with data["coalesced"] = "5" after the 50 ms window.
	for i := 1; i <= 5; i++ {
		ing.Submit(basePayload(uint64(i)))
	}

	// Wait past the coalesce window with a safety margin for CI slop.
	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("burst of 5 must coalesce to 1 emit; got %d", len(got))
	}
	if got[0].Seq != 5 {
		t.Errorf("last-wins: got Seq=%d, want 5 (newest in window)", got[0].Seq)
	}
	if got[0].Data["coalesced"] != "5" {
		t.Errorf("burst count: got Data[coalesced]=%q, want %q", got[0].Data["coalesced"], "5")
	}
}

func TestIngester_Submit_DifferentEventsDoNotCoalesce(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	a := basePayload(1)
	a.HookEvent = "Stop"
	b := basePayload(2)
	b.HookEvent = "PermissionRequest"

	ing.Submit(a)
	ing.Submit(b)

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 2 {
		t.Fatalf("two distinct hook_events: want 2 emits, got %d", len(got))
	}
}

func TestIngester_Submit_DifferentPanesDoNotCoalesce(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	a := basePayload(1)
	a.PaneID = "pane-a"
	b := basePayload(2)
	b.PaneID = "pane-b"

	ing.Submit(a)
	ing.Submit(b)

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 2 {
		t.Fatalf("two distinct panes: want 2 emits, got %d", len(got))
	}
}

func TestIngester_FlushAll_DrainsPendingImmediately(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	ing.Submit(basePayload(1))
	// Don't wait for the timer — FlushAll should emit immediately.
	ing.FlushAll()

	got := rec.drain()
	if len(got) != 1 {
		t.Errorf("FlushAll: got %d emits, want 1", len(got))
	}
}

func TestIngester_RateLimit_TripsAndEmitsStormDiagnostic(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	// Fire exactly rateWindowSize events distinct enough not to coalesce
	// (vary HookEvent so each lands as its own coalesce key — first 1ms
	// after each Submit they're independent rate counts).
	for i := 0; i < rateWindowSize; i++ {
		p := basePayload(uint64(i))
		p.HookEvent = "Event" + formatUint(uint64(i))
		ing.Submit(p)
	}

	// One more — must trip the storm.
	overflow := basePayload(uint64(rateWindowSize + 1))
	overflow.HookEvent = "Overflow"
	ing.Submit(overflow)

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	// Among the emits we should find exactly one storm diagnostic.
	stormCount := 0
	for _, p := range got {
		if p.HookEvent == "internal.event_storm" {
			stormCount++
		}
	}
	if stormCount != 1 {
		t.Errorf("storm diagnostics: got %d, want 1", stormCount)
	}

	// Further events from the same pane within the penalty window must be
	// dropped — they should NOT appear in subsequent emits.
	for i := 0; i < 10; i++ {
		p := basePayload(uint64(1000 + i))
		p.HookEvent = "Suppressed" + formatUint(uint64(i))
		ing.Submit(p)
	}
	time.Sleep(150 * time.Millisecond)
	tail := rec.drain()
	for _, p := range tail {
		if p.HookEvent != "internal.event_storm" {
			// Storm-period drops mean nothing-but-storms; if any other
			// emit slipped through during the penalty window, fail.
			t.Errorf("event during penalty window was not dropped: %+v", p)
		}
	}
}

func TestIngester_RateLimit_RecoversAfterPenalty(t *testing.T) {
	t.Parallel()
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	// Override the clock so we can advance through the penalty window
	// without waiting 10 real seconds.
	var nowMu sync.Mutex
	current := time.Unix(1700000000, 0)
	ing.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return current
	}

	// Trip the limiter.
	for i := 0; i <= rateWindowSize; i++ {
		p := basePayload(uint64(i))
		p.HookEvent = "E" + formatUint(uint64(i))
		ing.Submit(p)
	}
	rec.drain() // discard storm + initial emits

	// Advance past the penalty + the window for clean state.
	nowMu.Lock()
	current = current.Add(stormPenaltyDuration + rateWindowDuration + time.Second)
	nowMu.Unlock()

	// One submission AFTER recovery must succeed.
	p := basePayload(9999)
	p.HookEvent = "AfterRecovery"
	ing.Submit(p)
	ing.FlushAll()

	got := rec.drain()
	found := false
	for _, e := range got {
		if e.HookEvent == "AfterRecovery" {
			found = true
		}
	}
	if !found {
		t.Errorf("limiter must recover after penalty; AfterRecovery not emitted")
	}
}
