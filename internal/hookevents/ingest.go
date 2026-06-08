package hookevents

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// EventStorm is the canonical hook_event name for the synthetic diagnostic
// the rate limiter emits when a pane crosses the 100-events/2s budget. The
// daemon's emitHookEvent consumer references this constant so the
// HookHealthy bookkeeping can skip these self-emitted events — otherwise a
// rate-limited pane would falsely appear "hook-healthy" during the 10 s
// penalty window and the legacy idle excerpt (the user's last fallback)
// would also be silenced.
const EventStorm = "internal.event_storm"

// Ingester is the daemon-side gate between raw Spool / IPC payloads and the
// downstream emit callback that translates them to daemon.PaneEvent. It
// owns two flow-control mechanisms:
//
//  1. Per-pane sliding-window RATE LIMITER. A pane that emits > 100 events
//     in any 2-second window trips the limiter; further events from that
//     pane are dropped for 10 seconds and a single "storm" diagnostic is
//     synthesised so the user sees the problem rather than wondering where
//     the events went.
//
//  2. Per-(paneID, hook_event) COALESCER with a 50 ms debounce. When the
//     same logical event fires N times within the window (e.g. the
//     OpenCode "session.status busy → idle → busy → idle" flapping during
//     a tool call), only the LAST payload in the window is forwarded to
//     emit, with data["coalesced"] bumped to N. This keeps the eventQueue
//     and the IPC fan-out from being saturated by streaming-style events
//     while still surfacing the final state to the user.
//
// Both mechanisms layer on top of the eventQueue's existing same-
// (PaneID, Title) aggregation (×N badge). Coalescer collapses bursts of
// the SAME hook_event; queue aggregation collapses bursts where multiple
// hook_event types happen to map to the same human title.
type Ingester struct {
	emit func(Payload)

	// now is overridable for tests so we don't depend on wall-clock time
	// for rate-limiter / coalesce window assertions. Defaults to time.Now.
	now func() time.Time

	mu      sync.Mutex
	closed  bool                     // FlushAll set; future Submits no-op
	rates   map[string]*paneRate     // paneID → sliding window
	pending map[string]*pendingEvent // (paneID + "\x00" + hookEvent) → buffered coalesce
}

const (
	// rateWindowSize / rateWindowDuration form the per-pane budget. 100
	// events / 2 s is generous for healthy hook activity (a Claude turn
	// with 30 tool calls + a few state events is well under) and tight
	// enough that a runaway hook (test loop, broken pattern matcher)
	// trips within ~20 ms of starting.
	rateWindowSize     = 100
	rateWindowDuration = 2 * time.Second

	// stormPenaltyDuration is the dropoff after the limiter trips. Brief
	// enough that a healthy pane recovers visibly; long enough to suppress
	// log-stamp noise from a sustained issue.
	stormPenaltyDuration = 10 * time.Second

	// coalesceDelay is the per-(paneID, hook_event) debounce. The first
	// event arms a timer; subsequent events in the window replace the
	// buffered payload and DO NOT re-arm. When the timer fires the last
	// payload wins, with the burst count attached.
	coalesceDelay = 50 * time.Millisecond
)

type paneRate struct {
	// timestamps is a small sliding window (capacity rateWindowSize). On
	// each Submit we prune entries older than rateWindowDuration before
	// the size check. Implemented as a slice not a ring buffer because N
	// is tiny and the simplicity wins.
	timestamps []time.Time

	// dropUntil is the timestamp after which we accept events again. Zero
	// when the limiter is not currently tripped.
	dropUntil time.Time

	// droppedCount is the number of events suppressed during the current
	// penalty window. Reset to 0 when penalty clears.
	droppedCount int

	// stormReported is true once the synthesised "event storm" diagnostic
	// has been emitted for the current penalty window so we don't emit it
	// every single dropped event.
	stormReported bool
}

type pendingEvent struct {
	payload     Payload
	burstCount  int
	scheduledAt time.Time
	timer       *time.Timer
}

// NewIngester returns an Ingester whose Submit forwards through the rate
// limiter and coalescer to emit. The callback should be cheap (the daemon's
// emitEvent already has its own locking and broadcast machinery).
func NewIngester(emit func(Payload)) *Ingester {
	return &Ingester{
		emit:    emit,
		now:     time.Now,
		rates:   make(map[string]*paneRate),
		pending: make(map[string]*pendingEvent),
	}
}

// Submit accepts a payload for ingest. May be called from any goroutine.
// Returns immediately — the actual emit happens after the coalesce window
// closes (typically 50 ms later, sometimes immediately for the first event
// of a new (pane, hook_event) pair).
//
// Validation failures are silently dropped: Submit assumes the caller has
// already validated. The Spool reader does this before calling Submit;
// the IPC handler should do the same.
//
// After FlushAll has been called as part of daemon shutdown, Submit
// becomes a no-op so a late-arriving payload from a still-draining
// goroutine cannot leak past the documented "after FlushAll returns no
// emits will happen" guarantee.
func (i *Ingester) Submit(p Payload) {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return
	}
	i.mu.Unlock()
	now := i.now()
	if !i.allowAndRecord(p, now) {
		return
	}
	i.coalesce(p, now)
}

// Cancel discards any coalescer state for a pane being destroyed. Stops the
// AfterFunc timer for every pending event keyed by paneID and removes the
// per-pane rate-limiter bookkeeping. Critical for lifecycle correctness:
// without this, a pane destroyed while a 50 ms coalesce window is open
// would emit one final stale event ~50 ms later via the AfterFunc
// goroutine — at best a debug-log drop in emitHookEvent (pane gone), at
// worst a race window where the pane briefly exists again under a
// different identity (defensive — Quil's UUID pane IDs make actual reuse
// effectively impossible).
//
// Safe to call for a paneID that has no pending events; idempotent.
func (i *Ingester) Cancel(paneID string) {
	prefix := paneID + "\x00"
	i.mu.Lock()
	defer i.mu.Unlock()
	for k, p := range i.pending {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		delete(i.pending, k)
	}
	delete(i.rates, paneID)
}

// allowAndRecord returns true if the payload is within budget. If it
// exceeds budget it returns false; if it crosses the threshold it emits a
// synthesised "storm" diagnostic so the user is told about the drop.
func (i *Ingester) allowAndRecord(p Payload, now time.Time) bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	r, ok := i.rates[p.PaneID]
	if !ok {
		r = &paneRate{}
		i.rates[p.PaneID] = r
	}

	// If still inside the penalty window, drop.
	if !r.dropUntil.IsZero() && now.Before(r.dropUntil) {
		r.droppedCount++
		return false
	}
	// Clear penalty bookkeeping when window ends.
	if !r.dropUntil.IsZero() && !now.Before(r.dropUntil) {
		r.dropUntil = time.Time{}
		r.stormReported = false
		r.droppedCount = 0
	}

	// Prune timestamps older than the window.
	cutoff := now.Add(-rateWindowDuration)
	pruned := r.timestamps[:0]
	for _, ts := range r.timestamps {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	r.timestamps = pruned

	if len(r.timestamps) >= rateWindowSize {
		// Trip the storm. Emit one synthetic diagnostic before silencing.
		r.dropUntil = now.Add(stormPenaltyDuration)
		r.droppedCount = 1
		if !r.stormReported {
			r.stormReported = true
			// Build storm diagnostic — emit OUTSIDE the lock to avoid
			// reentrancy if emit calls back into the Ingester somehow.
			storm := stormPayload(p.PaneID, p.Source, now)
			i.mu.Unlock()
			i.emit(storm)
			i.mu.Lock()
		}
		return false
	}

	r.timestamps = append(r.timestamps, now)
	return true
}

// stormPayload synthesises the diagnostic event emitted when the per-pane
// rate limiter trips. Has no Validate-required fields wrong; reuses the
// PaneID and Source of the offending pane so it routes through the same
// downstream filters (mute, active-pane suppression, etc.).
func stormPayload(paneID, source string, now time.Time) Payload {
	return Payload{
		V:         SchemaVersion,
		TsMs:      now.UnixMilli(),
		Seq:       0,
		PaneID:    paneID,
		Source:    source,
		HookEvent: EventStorm,
		Title:     "Hook event storm — silenced 10 s",
		Severity:  SeverityWarning,
		Data: map[string]string{
			"reason": "rate_limit_exceeded",
		},
	}
}

// coalesce buffers a payload under its (paneID, hook_event) key. The first
// event in a new window arms the timer; subsequent events in the window
// replace the buffered payload and bump the burst counter. When the timer
// fires we emit the LAST buffered payload (so the freshest state wins) with
// the burst count attached so consumers can render ×N.
func (i *Ingester) coalesce(p Payload, now time.Time) {
	key := p.PaneID + "\x00" + p.HookEvent

	i.mu.Lock()
	pending, exists := i.pending[key]
	if exists {
		// Replace payload with the newer one, bump count, leave timer alone.
		pending.burstCount++
		pending.payload = p
		i.mu.Unlock()
		return
	}
	pending = &pendingEvent{
		payload:     p,
		burstCount:  1,
		scheduledAt: now,
	}
	pending.timer = time.AfterFunc(coalesceDelay, func() {
		i.flush(key)
	})
	i.pending[key] = pending
	i.mu.Unlock()
}

// flush emits the buffered payload for key and removes the pending entry.
// Called from the AfterFunc timer goroutine.
func (i *Ingester) flush(key string) {
	i.mu.Lock()
	pending, ok := i.pending[key]
	if !ok {
		i.mu.Unlock()
		return
	}
	delete(i.pending, key)
	i.mu.Unlock()

	payload := pending.payload
	if pending.burstCount > 1 {
		if payload.Data == nil {
			payload.Data = make(map[string]string)
		}
		payload.Data["coalesced"] = formatUint(uint64(pending.burstCount))
	}
	i.emit(payload)
}

// formatUint is a tiny strconv.FormatUint shim — kept inline so we don't
// pull strconv across the public boundary. uint64 because Payload.Seq is
// already uint64 and the same formatter handles both.
func formatUint(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}

// FlushAll is a test helper / shutdown helper that drains the coalescer's
// pending buffers immediately, emitting whatever is currently queued. The
// hookEventsWatcher goroutine calls it during shutdown so in-flight bursts
// surface before the IPC server tears down.
//
// FlushAll also marks the Ingester closed so any concurrent or
// late-arriving Submit becomes a no-op — without this, a Submit racing
// with shutdown could repopulate i.pending after the drain, then fire 50
// ms later when the daemon's emit pipeline has already torn down.
//
// Every pending timer is Stop()ped before drain so the AfterFunc
// goroutines do not fire a second redundant flush; the explicit flush
// loop covers what the timers would have delivered.
func (i *Ingester) FlushAll() {
	i.mu.Lock()
	i.closed = true
	keys := make([]string, 0, len(i.pending))
	for k, p := range i.pending {
		if p.timer != nil {
			p.timer.Stop()
		}
		keys = append(keys, k)
	}
	// Sort so the emit order is deterministic for tests.
	sort.Strings(keys)
	i.mu.Unlock()

	for _, k := range keys {
		i.flush(k)
	}
}
