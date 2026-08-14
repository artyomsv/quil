//go:build windows

// Command quil-activate delivers a desktop-toast click.
//
// It exists for ONE reason: it is linked with -H windowsgui, so Windows gives
// it no console. `quil.exe activate` — the handler this replaces — is a console
// binary, so launching it for a click allocated it a real, visible, focusable
// console window. That window appeared in front of the user, took the
// foreground, and made the handler compete with itself: it asked for the
// terminal to be raised, its own console took the foreground back, and when it
// exited the console was destroyed and left focus nowhere at all. Clicking a
// toast and then typing into neither window was that, and no amount of
// SetForegroundWindow technique could get around it, because the obstacle was
// the handler's own window.
//
// FreeConsole was tried first and is not sufficient: Windows creates the
// console during process startup, before any Go code runs, so the window can
// only be destroyed AFTER it has already appeared. The flicker survived. A
// process that must never show a window cannot be a console binary.
//
// Everything it may do is in notify.RunActivation: parse the URI, validate the
// pane id, send that id to the TUI over a per-PID pipe, raise the window. There
// is deliberately no path from a registered URI — which any local process can
// invoke — to spawning a pane, sending input, or running a command.
package main

import (
	"os"

	"github.com/artyomsv/quil/internal/notify"
)

func main() {
	scheme, home, raw := parseArgs(os.Args[1:])
	if raw == "" || scheme == "" {
		// Nothing to report and nowhere to report it: without --home there is
		// no log, and with no console there is no stderr. Exit 0 regardless —
		// a nonzero exit from a URI handler is a Windows error dialog, and a
		// stale or malformed toast must never produce one.
		return
	}
	notify.RunActivation(scheme, raw, notify.ActivationLogger(home))
}
