package proctree

import "time"

// KillGrace is how long a subtree gets to exit after a graceful signal before
// the sweep escalates.
const KillGrace = 3 * time.Second

// KillOps are the platform primitives the sweep drives.
//
// Every one of them takes a start time as well as a PID, and that is the whole
// safety model of this file: a PID alone does not identify a process. Each
// implementation is expected to PIN the identity where the platform allows it —
// open a handle or a pidfd, verify the start time on that, and signal the same
// object — so that nothing can be recycled between the check and the signal.
type KillOps struct {
	// Term asks a process to stop, only if it is still the process that had
	// the given start time.
	Term func(pid int, start time.Time) error
	// Kill stops a process forcibly, under the same identity condition.
	Kill func(pid int, start time.Time) error
	// Alive reports whether the process with this PID AND this start time is
	// still running. A recycled PID is not alive by this definition — it is a
	// different process.
	Alive func(pid int, start time.Time) bool
	// Sleep is the grace period wait, injected so tests do not take seconds.
	Sleep func(time.Duration)
}

// SweepResult reports what a kill actually did.
type SweepResult struct {
	// Signalled is how many processes were sent a graceful signal.
	Signalled int
	// Escalated is how many were still alive after the grace period and were
	// killed forcibly.
	Escalated int
	// Skipped is how many were not signalled because their identity could no
	// longer be confirmed — the PID-reuse case, counted rather than silently
	// dropped so it is visible in the logs.
	Skipped int
}

// Sweep terminates a subtree: graceful signal first, then a forced kill for
// whatever is genuinely still running after the grace period.
//
// Order is children before parents (see Flatten), so a parent cannot exit and
// reparent its children into the middle of the sweep.
//
// The escalation pass deliberately does NOT ask "is this PID alive". A subtree
// member can exit during the grace period and have its PID recycled, and
// killing on liveness alone would then destroy an unrelated process from inside
// the mechanism that exists to prevent exactly that. It asks whether the
// process WITH THAT START TIME is alive, which a recycled PID is not.
func Sweep(target *Node, grace time.Duration, ops KillOps) SweepResult {
	term := TermPass(target, ops)
	res := SweepResult{Signalled: term.Signalled, Skipped: term.Skipped}
	res.Escalated = Escalate(term, grace, ops)
	return res
}

// pendingKill is one process that was successfully signalled, remembered with
// the start time it had at that moment.
type pendingKill struct {
	pid   int
	start time.Time
}

// TermResult is what the graceful pass achieved, and the input to Escalate.
type TermResult struct {
	Signalled int
	Skipped   int
	termed    []pendingKill
}

// TermPass sends the graceful signal to a subtree and returns immediately.
//
// Split from the escalation so the daemon can answer the client with what
// actually happened — it completes in microseconds — while the grace period and
// the forced kill run on their own goroutine rather than parking an IPC
// dispatch goroutine for three seconds.
func TermPass(target *Node, ops KillOps) TermResult {
	var res TermResult
	if target == nil || ops.Term == nil {
		return res
	}
	for _, n := range Flatten(target) {
		if err := ops.Term(n.PID, n.Start); err != nil {
			// Either it already exited or its identity no longer matches.
			// Both mean "do not chase this PID any further".
			res.Skipped++
			continue
		}
		res.Signalled++
		res.termed = append(res.termed, pendingKill{pid: n.PID, start: n.Start})
	}
	return res
}

// Escalate waits out the grace period and forcibly kills whatever is still the
// same process. Returns how many were killed.
//
// Asks whether the process WITH THAT START TIME is alive, never whether the PID
// is in use: a subtree member can exit during the grace period and have its PID
// recycled, and killing on liveness alone would destroy an unrelated process
// from inside the mechanism meant to prevent exactly that.
func Escalate(r TermResult, grace time.Duration, ops KillOps) int {
	if len(r.termed) == 0 {
		return 0
	}
	if ops.Sleep != nil {
		ops.Sleep(grace)
	}
	var killed int
	for _, p := range r.termed {
		if ops.Alive == nil || !ops.Alive(p.pid, p.start) {
			continue
		}
		if ops.Kill == nil {
			continue
		}
		if err := ops.Kill(p.pid, p.start); err == nil {
			killed++
		}
	}
	return killed
}
