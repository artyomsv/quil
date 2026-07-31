package daemon

import "time"

// rootProbeTimeout bounds ONE root probe.
//
// Deliberately much shorter than the listing budget: this is a liveness check,
// not a listing, and a root that cannot answer it in a second is exactly the
// root this sweep exists to omit. Without a per-probe bound, a single
// disconnected mapping would spend the whole shared deadline before the
// directory the user actually asked for was ever read.
const rootProbeTimeout = time.Second

// maxRootProbeTimeouts bounds how many probes in one sweep may go unanswered
// before the sweep gives up.
//
// This is the bound the per-probe budget above cannot provide, and the reason it
// is needed is a property every OTHER blocking call site here gets for free.
// Elsewhere a single deadline is shared by every call in a response, so a call
// that times out has by definition spent the budget and every later one is
// refused without spawning anything — one hung path costs one abandoned
// goroutine for the whole response. This sweep is the sole exception: each probe
// gets its own rootProbeTimeout sub-budget, so timing out does NOT exhaust the
// shared deadline and the next probe starts a fresh abandoned goroutine.
//
// That matters because an abandoned probe keeps its maxBlockingFSCalls permit
// until the kernel call returns, and those permits are process-wide. A Windows
// host with eight dead network mappings — a VPN-off laptop with departmental
// shares is enough — would consume the entire pool in a single sweep, after
// which directory browsing, git discovery and kube discovery are all refused
// daemon-wide until a stalled probe completes. One user action pressing "up"
// from C:\ could deny the whole filesystem subsystem.
//
// Counting unanswered probes rather than elapsed time is what makes the bound
// tight: a probe that ANSWERS releases its permit immediately and accumulates
// nothing, however many of those there are. Only the abandoned ones cost, so
// they are what is capped. The cap also covers a refused claim, which
// statIsDirWithin reports as unanswered too — if the pool is already exhausted
// by someone else, adding more probes to the queue is the wrong move.
const maxRootProbeTimeouts = 2

// maxRootProbePermits is the ceiling the sweep claims against, leaving the rest
// of maxBlockingFSCalls as a reserve it can never touch.
//
// The per-sweep cap above is not sufficient on its own, and the arithmetic is
// the argument: it bounds ONE sweep at maxRootProbeTimeouts abandoned calls, but
// nothing returns those permits except the kernel finally answering, so
// ⌈maxBlockingFSCalls / maxRootProbeTimeouts⌉ successive sweeps still drain the
// pool. Pressing "up" from C:\ four times is not an attack, it is navigation.
//
// The reserve is what makes the drain unreachable rather than merely slower. It
// also encodes a priority: the root list is a speculative affordance for the
// "up" row, while the directory read that follows it is what the user actually
// asked for, so the sweep must never be able to starve that read. A sweep
// refused a permit reports an incomplete list, which the caller passes on.
const maxRootProbePermits = maxBlockingFSCalls / 2

// driveLetters is the Windows candidate set: every letter, in order, because a
// drive map is arbitrary and there is no cheaper way to learn which exist that
// does not reintroduce the GetLogicalDrives problem filesystemRoots describes.
//
// Tag-free despite being Windows-only in effect, so the tests exercise THIS
// function rather than a copy of it. A duplicate in the test file would keep
// passing if this one changed — skipping A:/B:, reversing order, adding volume
// GUID paths — which is the same reason sweepRoots was extracted at all. It is a
// pure string loop with no platform dependency, so nothing is lost by moving it.
func driveLetters() []string {
	out := make([]string, 0, 26)
	for c := 'A'; c <= 'Z'; c++ {
		out = append(out, string(c)+`:\`)
	}
	return out
}

// probeRoot is the real probe: a bounded stat that additionally respects the
// reserve above.
func probeRoot(path string, d time.Duration) (isDir, answered bool) {
	return statIsDirWithinLimit(path, d, maxRootProbePermits)
}

// sweepRoots probes each candidate and returns those that answer as directories.
//
// Split from the Windows-only caller on purpose. The drive-letter alphabet is
// platform-specific, but the budget arithmetic and the two give-up conditions
// are not, and the previous version of this logic lived entirely behind a
// //go:build windows tag that the Linux CI image never compiles. Its bounds were
// therefore unreachable by any test, and that is precisely where the permit
// exhaustion above went unnoticed. probe is a parameter for the same reason:
// statIsDirWithin talks to a real filesystem, and no test can produce a drive
// that parks in the kernel on demand.
//
// A partial list is returned on either give-up path rather than none: the roots
// found so far are still navigable. complete reports whether every candidate
// gave a DEFINITIVE answer — not merely whether the loop reached the end.
// A drive that failed to answer is absent from the result for a reason the user
// cannot see, exactly like one dropped past the cap, so both make the list
// partial. The caller must pass that on: the drives are shown AS the listing
// when the user navigates up, and "my D: drive vanished" is otherwise how a
// network problem presents. Saying nothing would be the same confidently-wrong
// answer that asking the daemon about its own filesystem exists to remove.
func sweepRoots(candidates []string, deadline time.Time, probe func(string, time.Duration) (isDir, answered bool)) (roots []string, complete bool) {
	stuck := 0
	for _, root := range candidates {
		budget := time.Until(deadline)
		if budget <= 0 {
			// The sweep has spent the response's whole budget.
			return roots, false
		}
		if budget > rootProbeTimeout {
			budget = rootProbeTimeout
		}
		isDir, answered := probe(root, budget)
		if !answered {
			stuck++
			if stuck >= maxRootProbeTimeouts {
				return roots, false
			}
			continue
		}
		if isDir {
			roots = append(roots, root)
		}
	}
	return roots, stuck == 0
}
