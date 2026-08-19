package daemon

import (
	"log"
	"time"

	apty "github.com/artyomsv/quil/internal/pty"
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
// redrawKeyCooldown.
//
// Leading edge immediate, trailing edge coalesced. Immediate because the attach
// kick exists precisely for a pane that is blank RIGHT NOW; coalesced because a
// suppressed kick can be the one that mattered — a resize kick dropped after an
// attach kick leaves the pane painted at the old width, which is the
// overlapping-banner report repaintAfterResize was added for.
//
// EVERY redraw-key delivery goes through here. A direct EnqueueInput of a
// redraw key elsewhere reintroduces issue #169 with every test still green.
func (d *Daemon) sendRedrawKey(pane *Pane, typ, key string) {
	if key == "" {
		return
	}
	now := time.Now()

	pane.PluginMu.Lock()
	elapsed := now.Sub(pane.lastRedrawAt)
	immediate := elapsed >= redrawKeyCooldown
	switch {
	case immediate:
		pane.lastRedrawAt = now
	case pane.redrawTimer == nil:
		// Captured under the lock: deferredRedrawKey compares this against the
		// pane's PTY when it fires, so a restart in between is detectable.
		pty := pane.PTY
		wait := redrawKeyCooldown - elapsed
		pane.redrawTimer = time.AfterFunc(wait, func() {
			d.deferredRedrawKey(pane, typ, key, pty)
		})
		log.Printf("pane %s: redraw key held %v (type=%s, cooldown)",
			pane.ID, wait.Round(time.Millisecond), typ)
	}
	pane.PluginMu.Unlock()

	// Outside the lock, same discipline as handleResizePane's Resize syscall:
	// the enqueue hands off to the pane's writer goroutine, which takes
	// PluginMu itself.
	if immediate {
		recordRedrawEnqueue(pane, key)
	}
}

// recordRedrawEnqueue delivers a kick and remembers where it sits in the input
// queue, so a later kick can tell whether the child has actually received this
// one. A refused enqueue (full queue) records nothing: that byte will never be
// written, so waiting on its position would stall every later kick forever.
func recordRedrawEnqueue(pane *Pane, key string) {
	seq, ok := pane.enqueueInputSeq([]byte(key))
	if !ok {
		return
	}
	pane.PluginMu.Lock()
	pane.redrawSeq = seq
	pane.PluginMu.Unlock()
}

// deferredRedrawKey delivers the single kick sendRedrawKey held back.
//
// pty is the session that was installed when the timer was armed. Comparing it
// against the current one is how a restart is detected: every apty.Session
// implementation is a pointer, so this is identity rather than equality.
func (d *Daemon) deferredRedrawKey(pane *Pane, typ, key string, pty apty.Session) {
	pane.PluginMu.Lock()
	pane.redrawTimer = nil
	// The pane can die, be torn down, or be restarted while the timer is armed.
	// A restarted pane gets its own kick from the attach and resize paths, so
	// delivering a stale one only risks pairing with THAT one. inputStopped is
	// the teardown case specifically: releasePanes leaves PTY and ExitCode
	// untouched, so the other two checks see a perfectly healthy pane.
	stale := pane.PTY == nil || pane.PTY != pty || pane.ExitCode != nil || pane.inputStopped
	// The cooldown elapsed on OUR clock. It means nothing unless the earlier
	// kick actually reached the child: a child that has stopped reading its
	// stdin (the wedge this daemon has hit in production) leaves that byte
	// queued, and delivering a second one behind it hands the child both at
	// once the moment it resumes — which is the /clear chord this whole
	// mechanism exists to avoid.
	delivered := pane.inputWritten.Load() >= pane.redrawSeq
	if !stale && delivered {
		pane.lastRedrawAt = time.Now()
	}
	pane.PluginMu.Unlock()

	if stale {
		return
	}
	if !delivered {
		// DROPPED, not re-armed, and nothing is lost by it: the queued kick is
		// itself the repaint, and it will be read at the child's current
		// geometry — the resize that asked for this one already reached the
		// PTY as a SIGWINCH. Re-arming would only rebuild the pair later.
		log.Printf("pane %s: held redraw key dropped (type=%s, earlier kick still queued)", pane.ID, typ)
		return
	}
	log.Printf("pane %s: held redraw key delivered (type=%s)", pane.ID, typ)
	recordRedrawEnqueue(pane, key)
}
