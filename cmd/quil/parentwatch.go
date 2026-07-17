package main

import "time"

// parentHandleTrustworthy reports whether a process opened via the recorded
// parent PID is plausibly the ORIGINAL parent rather than a PID-reuse
// impostor: a real parent was created no later than the child it spawned
// (equal timestamps are possible at coarse clock resolution). When this
// returns false the original parent is already gone — the watchdog must
// treat the parent as dead instead of waiting on a stranger.
func parentHandleTrustworthy(parentCreated, selfCreated time.Time) bool {
	return !parentCreated.After(selfCreated)
}
