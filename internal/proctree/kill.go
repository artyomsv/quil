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
	var res SweepResult
	if target == nil {
		return res
	}

	nodes := Flatten(target)

	type pending struct {
		pid   int
		start time.Time
	}
	var termed []pending

	for _, n := range nodes {
		if ops.Term == nil {
			break
		}
		if err := ops.Term(n.PID, n.Start); err != nil {
			// Either it already exited or its identity no longer matches.
			// Both mean "do not chase this PID any further".
			res.Skipped++
			continue
		}
		res.Signalled++
		termed = append(termed, pending{pid: n.PID, start: n.Start})
	}

	if len(termed) == 0 {
		return res
	}

	if ops.Sleep != nil {
		ops.Sleep(grace)
	}

	for _, p := range termed {
		if ops.Alive == nil || !ops.Alive(p.pid, p.start) {
			continue
		}
		if ops.Kill == nil {
			continue
		}
		if err := ops.Kill(p.pid, p.start); err == nil {
			res.Escalated++
		}
	}
	return res
}
