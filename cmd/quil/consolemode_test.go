package main

import "testing"

// Both functions run on every launch, including ones where stdio is a pipe or a
// file — `go test`, CI, `quil ... | tee`, and any harness-spawned invocation. A
// panic or a hang there would break startup for reasons unrelated to what the
// user asked for, so the no-console path is the one worth pinning.
//
// On Windows the handle probes fail and each entry is recorded as not-a-console;
// off Windows both calls are declared no-ops. Either way: silent.
func TestConsoleMode_SaveThenRestore_SafeWithoutAConsole(t *testing.T) {
	saveConsoleMode()
	restoreConsoleMode()
	// Called once per diagnostic site, and two can run in sequence on the
	// dead-link path — the install offer, then the failure report.
	restoreConsoleMode()
}

// restoreConsoleMode is also reachable with nothing saved: the version gate
// calls it, and the gate's own tests drive that path directly without going
// through main. Treating that as "nothing to do" is the correct behaviour.
func TestConsoleMode_RestoreWithNothingSaved_IsNoop(t *testing.T) {
	restoreConsoleMode()
}
