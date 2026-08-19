package daemon

import (
	"log"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// redrawKeyCooldown is the minimum interval between two deliveries of a
// plugin's redraw_key to the same pane.
//
// A redraw_key is INPUT rather than a signal — that is why it is opt-in per
// plugin. The opt-in was a contract about ONE byte, and claude-code broke it in
// v2.1.126 by giving a REPEATED press a second meaning: in fullscreen
// rendering, two Ctrl+L within two seconds run /clear and start a new
// conversation (code.claude.com/docs/en/fullscreen). Quil's two kick sites,
// redrawKick on attach and repaintAfterResize on a genuine resize, fire
// milliseconds apart on an ordinary reattach — measured at 5.7 ms — which wiped
// the conversation in every AI pane at once, unattended (issue #169).
//
// Three seconds rather than two: the spacing has to EXCEED the receiver's
// window with margin for queue latency and clock granularity, and nothing is
// lost by waiting, since a repaint that lands 3 s after a burst still lands.
//
// A package var so tests can shrink it. The shipped value is pinned by
// TestRedrawKeyCooldown_ExceedsClaudeClearWindow — the number IS the fix.
var redrawKeyCooldown = 3 * time.Second

// sendRedrawKey delivers a plugin's redraw_key to a pane, never more often than
// redrawKeyCooldown and never while the previous one is still queued.
//
// Leading edge immediate, trailing edge coalesced. Immediate because the attach
// kick exists precisely for a pane that is blank RIGHT NOW; coalesced because a
// suppressed kick can be the one that mattered — a resize kick dropped after an
// attach kick leaves the pane painted at the old width, which is the
// overlapping-banner report repaintAfterResize was added for.
//
// The elapsed-time test alone is NOT sufficient, and the reason is the same one
// that makes issue #169 exist: the window belongs to the CHILD. This stamps at
// enqueue, while the byte reaches the child whenever inputWriter drains it — so
// a child that has stopped reading its stdin (the 2026-06-11/12 wedge the input
// queue exists for) holds the earlier kick and receives both back to back the
// moment it resumes, microseconds apart on its own clock. Measured during
// review: two kicks enqueued 250 ms apart behind a blocked Write arrived
// 17.9 µs apart. Hence the queued check, on BOTH paths.
//
// EVERY redraw-key delivery goes through here. A direct EnqueueInput of a
// redraw key elsewhere reintroduces issue #169 with every test still green.
func (d *Daemon) sendRedrawKey(pane *Pane, typ, key string) {
	if key == "" {
		return
	}
	now := time.Now()

	pane.PluginMu.Lock()
	// queued: the previous kick has not left the input queue, so the child has
	// not seen it and a second one would arrive alongside it. It gates SENDING
	// only — never arming. A kick enqueued microseconds ago is still queued in
	// the ordinary case (the writer goroutine has not run yet), and treating
	// that as "skip" would drop the resize kick on every reattach, which is the
	// stale-geometry paint repaintAfterResize exists to prevent.
	queued := pane.inputWritten.Load() < pane.redrawSeq
	elapsed := now.Sub(pane.lastRedrawAt)
	due := elapsed >= redrawKeyCooldown
	immediate := due && !queued
	var wait time.Duration
	switch {
	case immediate:
		pane.lastRedrawAt = now
	case pane.redrawTimer == nil:
		// The generation, not the session pointer: deferredRedrawKey compares
		// it to detect a restart, and an interface comparison would panic on
		// any future value-receiver implementation of apty.Session — inside a
		// time.AfterFunc goroutine, where a panic takes the whole daemon.
		gen := pane.ptyGen
		wait = redrawKeyCooldown - elapsed
		if wait <= 0 {
			// Due, but the child has not read the previous kick. Come back a
			// full cooldown later rather than sending now; the deferred path
			// re-checks and drops if the child is still behind.
			wait = redrawKeyCooldown
		}
		pane.redrawTimer = time.AfterFunc(wait, func() {
			d.deferredRedrawKey(pane, typ, key, gen)
		})
	}
	pane.PluginMu.Unlock()

	// Everything below runs OUTSIDE the lock. The enqueue hands off to the
	// pane's writer goroutine, which takes PluginMu itself; and a log write is
	// I/O, which this mutex is never held across.
	switch {
	case immediate:
		recordRedrawEnqueue(pane, typ, key)
	case due && queued:
		logger.Debug("pane %s: redraw key deferred (type=%s, earlier kick still queued)", pane.ID, typ)
	case wait > 0:
		log.Printf("pane %s: redraw key held %v (type=%s, cooldown)",
			pane.ID, wait.Round(time.Millisecond), typ)
	}
}

// recordRedrawEnqueue delivers a kick and remembers where it sits in the input
// queue, so a later kick can tell whether the child has actually received it.
func recordRedrawEnqueue(pane *Pane, typ, key string) {
	seq, ok := pane.enqueueInputSeq([]byte(key))
	if !ok {
		// A refused enqueue records nothing: that byte will never be written,
		// so waiting on its position would stall every later kick forever. The
		// stamp is deliberately left in place — a full queue means the child is
		// 256 messages behind and is painting nothing either way, so retrying
		// the nudge on every resize would only add to the backlog.
		logger.Debug("pane %s: redraw key dropped (type=%s, input queue full)", pane.ID, typ)
		return
	}
	pane.PluginMu.Lock()
	pane.redrawSeq = seq
	pane.PluginMu.Unlock()
}

// deferredRedrawKey delivers the single kick sendRedrawKey held back.
//
// gen is the pane's PTY generation at the moment the timer was armed. Comparing
// generations rather than session pointers is how a restart is detected without
// an interface comparison that a value-receiver implementation could turn into
// a panic on this goroutine.
func (d *Daemon) deferredRedrawKey(pane *Pane, typ, key string, gen uint64) {
	pane.PluginMu.Lock()
	pane.redrawTimer = nil
	// The pane can die, be torn down, or be restarted while the timer is armed.
	// A restarted pane gets its own kick from the attach and resize paths, so
	// delivering a stale one only risks pairing with THAT one. inputStopped is
	// the teardown case specifically: releasePanes leaves PTY and ExitCode
	// untouched, so the other checks see a perfectly healthy pane.
	stale := pane.PTY == nil || pane.ptyGen != gen || pane.ExitCode != nil || pane.inputStopped
	delivered := pane.inputWritten.Load() >= pane.redrawSeq
	// The stamp may have moved since this timer was armed: markInputWritten
	// restarts the cooldown when the earlier kick actually reached the child,
	// which is later than the enqueue this was scheduled against. Waiting out
	// the remainder is the difference between "three seconds by our clock" and
	// "three seconds by the child's".
	remaining := redrawKeyCooldown - time.Since(pane.lastRedrawAt)
	if !stale && delivered && remaining <= 0 {
		pane.lastRedrawAt = time.Now()
	} else if !stale && delivered {
		pane.redrawTimer = time.AfterFunc(remaining, func() {
			d.deferredRedrawKey(pane, typ, key, gen)
		})
	}
	pane.PluginMu.Unlock()

	if stale {
		return
	}
	if delivered && remaining > 0 {
		logger.Debug("pane %s: held redraw key re-armed for %v (type=%s, child read the last one late)",
			pane.ID, remaining.Round(time.Millisecond), typ)
		return
	}
	if !delivered {
		// DROPPED, not re-armed, and nothing is lost by it: the queued kick is
		// itself the repaint, and it will be read at the child's current
		// geometry — the resize that asked for this one already reached the PTY
		// as a SIGWINCH. Re-arming would only rebuild the pair later.
		logger.Debug("pane %s: held redraw key dropped (type=%s, earlier kick still queued)", pane.ID, typ)
		return
	}
	log.Printf("pane %s: held redraw key delivered (type=%s)", pane.ID, typ)
	recordRedrawEnqueue(pane, typ, key)
}
