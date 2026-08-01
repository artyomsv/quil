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

func TestIngester_Submit_DifferentSubagentsDoNotCoalesce(t *testing.T) {
	t.Parallel()
	// The TUI's work ledger matches a SubagentStop to the SubagentStart that
	// names the same agent. Coalescing is last-wins, so merging two DIFFERENT
	// agents' starts into one payload would erase the loser's identity: its
	// own stop then matches nothing and the winner's count never drains,
	// wedging the spinner. Distinct agent_type values must stay distinct.
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	a := basePayload(1)
	a.HookEvent = "SubagentStart"
	a.Data = map[string]string{"agent_type": "impl-task7"}
	b := basePayload(2)
	b.HookEvent = "SubagentStart"
	b.Data = map[string]string{"agent_type": "rev-task7"}

	ing.Submit(a)
	ing.Submit(b)

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 2 {
		t.Fatalf("two distinct agent types: want 2 emits, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.Data["agent_type"]] = true
	}
	for _, want := range []string{"impl-task7", "rev-task7"} {
		if !seen[want] {
			t.Errorf("agent_type %q was coalesced away; emitted set: %v", want, seen)
		}
	}
}

func TestIngester_Submit_PhantomStopDoesNotSwallowNamedStop(t *testing.T) {
	t.Parallel()
	// The highest-value coalescing case: a NAMED SubagentStop followed by the
	// unpaired end-of-turn stop (empty agent_type) inside one window. Under a
	// key without agent_type these merged last-wins and the phantom WON,
	// destroying the named drain on the wire — the ledger would then never
	// clear that agent and the spinner would wedge ON, the mirror of the bug
	// this branch fixes. Both must survive as separate emits.
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	named := basePayload(1)
	named.HookEvent = "SubagentStop"
	named.Data = map[string]string{"agent_type": "Explore"}
	phantom := basePayload(2)
	phantom.HookEvent = "SubagentStop"
	phantom.Data = map[string]string{"agent_type": ""}

	ing.Submit(named)
	ing.Submit(phantom)

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 2 {
		t.Fatalf("named stop and phantom stop merged: want 2 emits, got %d", len(got))
	}
	var sawNamed bool
	for _, p := range got {
		if p.Data["agent_type"] == "Explore" {
			sawNamed = true
		}
	}
	if !sawNamed {
		t.Error("the named agent's stop was coalesced away by the phantom — its ledger entry would never drain")
	}
}

func TestIngester_Submit_ControlCharsCannotForgeCoalesceKey(t *testing.T) {
	t.Parallel()
	// The coalesce key joins two FREE-FORM payload fields (hook_event and
	// agent_type) with a NUL separator, and JSON admits U+0000 in either. A
	// naive join lets two logically DIFFERENT events produce the same key —
	// which coalesces them last-wins and erases one identity, precisely the
	// failure this ledger exists to prevent. Pane IDs are NUL-free by
	// validation (safePaneID), so only the hook_event/agent_type boundary is
	// at risk; the key build must stay injective across it.
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	a := basePayload(1)
	a.HookEvent = "SubagentStart"
	a.Data = map[string]string{"agent_type": "\x00X"}
	b := basePayload(2)
	b.HookEvent = "SubagentStart\x00"
	b.Data = map[string]string{"agent_type": "X"}

	ing.Submit(a)
	ing.Submit(b)

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 2 {
		t.Fatalf("distinct (hook_event, agent_type) pairs collided into one coalesce key: want 2 emits, got %d", len(got))
	}
	for _, p := range got {
		if p.Data["coalesced"] != "" {
			t.Errorf("neither event should have been merged; got coalesced=%q", p.Data["coalesced"])
		}
	}
}

func TestIngester_Submit_SameSubagentTypeStillCoalesces(t *testing.T) {
	t.Parallel()
	// Three instances of the SAME agent spawning in one window is a real
	// parallel fan-out: it must still collapse to one emit carrying the burst
	// count, so the ledger records 3 outstanding rather than 1.
	rec := &emitRecorder{}
	ing := NewIngester(rec.emit)

	for i := 1; i <= 3; i++ {
		p := basePayload(uint64(i))
		p.HookEvent = "SubagentStart"
		p.Data = map[string]string{"agent_type": "Explore"}
		ing.Submit(p)
	}

	time.Sleep(150 * time.Millisecond)

	got := rec.drain()
	if len(got) != 1 {
		t.Fatalf("same agent type burst: want 1 emit, got %d", len(got))
	}
	if got[0].Data["coalesced"] != "3" {
		t.Errorf("burst count: got %q, want %q", got[0].Data["coalesced"], "3")
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
