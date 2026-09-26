package daemon

import "time"

// broadcastCoalesceWindow is requestBroadcast's trailing-edge coalescing
// window. Every call that lands within this window of the one that opened it
// rides the same timer; the broadcast fires once, at the end of the window,
// not once per call.
const broadcastCoalesceWindow = 50 * time.Millisecond

// requestBroadcast coalesces a burst of accepted layout writes into one
// broadcastState() call. A burst of SetTabLayout acceptances — several tabs
// re-sent after a split-drag release, or several clients editing at once —
// would otherwise put one full workspace-state frame per call onto every
// attached client's must-deliver queue, which is the documented 2026-08-09
// critical-queue shape (see "A per-pane broadcast is a queue-pressure
// decision, not a detail" in daemon-lifecycle.md).
//
// It is single-flighted under broadcastMu: the first call in a burst arms a
// timer and every call arriving while that timer is still pending returns
// immediately, folded into the same eventual broadcast for free.
func (d *Daemon) requestBroadcast() {
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	if d.broadcastTimerStop != nil {
		// A window is already open — this call rides it rather than arming
		// a second timer.
		return
	}
	after := d.broadcastAfterFn
	if after == nil {
		after = realAfterFunc
	}
	d.broadcastTimerStop = after(broadcastCoalesceWindow, d.fireCoalescedBroadcast)
}

// fireCoalescedBroadcast is requestBroadcast's timer callback. It clears the
// in-flight marker BEFORE calling broadcastState, so a requestBroadcast call
// racing this one opens a fresh window instead of silently riding a timer
// that has already fired.
func (d *Daemon) fireCoalescedBroadcast() {
	d.broadcastMu.Lock()
	d.broadcastTimerStop = nil
	d.broadcastMu.Unlock()
	// broadcastState nil-guards on d.server itself, which covers a Daemon
	// built for tests with no server at all — that guard is what makes this
	// safe to fire after Stop() has already run everything except the final
	// server teardown ordering. stopBroadcastCoalescer below is what stops it
	// from firing AT ALL once shutdown has begun disarming timers.
	d.broadcastState()
}

// stopBroadcastCoalescer disarms a pending coalesced broadcast at daemon
// shutdown, so it never fires into a stopped daemon. Mirrors
// clientRegistry.stopTimer, which the same Stop() call disarms alongside it.
func (d *Daemon) stopBroadcastCoalescer() {
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	if d.broadcastTimerStop != nil {
		d.broadcastTimerStop()
		d.broadcastTimerStop = nil
	}
}
