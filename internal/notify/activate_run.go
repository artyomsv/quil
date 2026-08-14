package notify

import "fmt"

// RunActivation delivers one toast click: route it to the TUI, then raise that
// TUI's terminal window.
//
// Lives here rather than in a command so the two entry points cannot drift.
// `quil activate` is the original handler and stays for installs whose registry
// still points at it; `quil-activate` is the windowless helper setup registers
// now. A click must mean exactly the same thing whichever one Windows launches,
// and the security ceiling — parse, validate, send a pane id, raise a window —
// has to be stated once.
//
// logf is injected rather than chosen here because the two callers resolve
// their log directory differently: the TUI binary knows QUIL_HOME from its own
// build flags, while the helper is told at registration time.
//
// Every path logs, including the ones that used to return silently. A click
// that reaches a TUI which has since exited is an ordinary, expected outcome —
// but "nothing happened and nothing was written" is indistinguishable from a
// handler that never ran, and this feature has cost enough hours to that
// exact ambiguity.
func RunActivation(scheme, raw string, logf func(string, ...any)) {
	pid, paneID, err := ParseActivateURI(scheme, raw)
	if err != nil {
		// The URI is attacker-reachable: any local process can invoke a
		// registered scheme. Refusing loudly here is the whole trust boundary.
		logf("refused malformed activation URI: %v", err)
		return
	}

	// A TUI that exited between the toast being shown and the click leaves no
	// listener. Not an error the user should see — a stale toast must never pop
	// a message box — but worth a line.
	if err := SendActivate(pid, paneID); err != nil {
		logf("no listener for pane %s (pid %d): %v", paneID, pid, err)
		return
	}

	// AFTER the send, so the jump is already applied by the time the window
	// appears and the user sees the right pane rather than watching it switch.
	how, err := RaiseWindowFor(pid)
	if err != nil {
		logf("raise failed for pane %s: %v (%s)", paneID, err, foregroundLockNote())
		return
	}
	logf("raised pid %d for pane %s via %s", pid, paneID, how)
}

// foregroundLockNote renders the foreground lock timeout in the three states it
// really has: a value, zero, or unknown.
//
// Named IN the failure line rather than left for someone to look up, because it
// is the usual explanation and it is per-user state no other log records. A
// refusal with a non-zero timeout is Windows working as configured; a refusal
// with zero is a genuinely different problem, and the line has to say which.
func foregroundLockNote() string {
	d, err := ForegroundLockTimeout()
	switch {
	case err != nil:
		return fmt.Sprintf("foreground lock timeout unknown: %v", err)
	case d == 0:
		return "foreground lock timeout is 0, so this refusal is NOT the lock"
	default:
		return fmt.Sprintf("foreground lock timeout is %s — Windows is refusing focus by policy", d)
	}
}
