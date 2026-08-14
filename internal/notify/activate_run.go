package notify

// RunActivation delivers one toast click: it routes that click to the TUI.
//
// Lives here rather than in a command so the two entry points cannot drift.
// `quil activate` is the original handler and stays for installs whose registry
// still points at it; `quil-activate` is the windowless helper setup registers
// now. A click must mean exactly the same thing whichever one Windows launches,
// and the security ceiling — parse, validate, send a pane id — has to be stated
// once.
//
// It does NOT bring the terminal window forward, and that is a decision rather
// than an omission. Clicking a toast hands the foreground to the shell's
// notification host, which keeps it for as long as it likes — measured on one
// machine at 5.5 s in one click and over 10 s in the next, released on user
// input rather than on a timer. Every documented way of taking it back is
// refused for as long as the shell holds it, and by the time it lets go the
// user has moved on, so a window appearing then would be an interruption rather
// than a service. Windows highlights the taskbar button instead, which is its
// sanctioned way of saying an application wants attention. The pane is already
// switched and focused by then, so switching to the terminal lands the user
// exactly where the toast was about to send them.
//
// logf is injected rather than chosen here because the two callers resolve
// their log directory differently: the TUI binary knows QUIL_HOME from its own
// build flags, while the helper is told at registration time.
//
// Every path logs, including the ones that would otherwise return silently. A
// click that reaches a TUI which has since exited is an ordinary, expected
// outcome — but "nothing happened and nothing was written" is indistinguishable
// from a handler that never ran, and this feature has cost enough hours to that
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
	logf("routed pane %s to pid %d", paneID, pid)
}
