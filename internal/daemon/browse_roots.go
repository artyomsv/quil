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
// found so far are still navigable, and the directory read that follows reports
// its own timeout.
func sweepRoots(candidates []string, deadline time.Time, probe func(string, time.Duration) (isDir, answered bool)) []string {
	var roots []string
	stuck := 0
	for _, root := range candidates {
		budget := time.Until(deadline)
		if budget <= 0 {
			// The sweep has spent the response's whole budget.
			break
		}
		if budget > rootProbeTimeout {
			budget = rootProbeTimeout
		}
		isDir, answered := probe(root, budget)
		if !answered {
			stuck++
			if stuck >= maxRootProbeTimeouts {
				break
			}
			continue
		}
		if isDir {
			roots = append(roots, root)
		}
	}
	return roots
}
