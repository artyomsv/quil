package notify

import (
	"errors"
	"strings"
)

var (
	// ErrUnsupported is returned by the setup commands on a platform with no
	// toast support. New() does NOT return it — it returns a nil Notifier, so
	// callers never branch on platform.
	ErrUnsupported = errors.New("notify: not supported on this platform")

	// ErrNotRegistered means the AUMID has no backing Start Menu shortcut, so
	// Windows will refuse to display a toast.
	ErrNotRegistered = errors.New("notify: run 'quil notify setup' first")
)

// Notifier raises and withdraws operating-system notifications.
//
// Withdraw is on the interface from the start rather than added later:
// Windows toasts persist in Action Center indefinitely, so without it,
// answering a prompt leaves a toast still claiming the pane needs attention.
//
// Both methods QUEUE work and return immediately. The returned error reports
// only whether the work could be queued — never whether a toast was displayed.
//
// That asymmetry is deliberate and load-bearing. The only TUI caller runs on
// Bubble Tea's Update goroutine, and a toast is seven cross-process WinRT
// round-trips to WpnUserService — a shared user service that this package has
// measured stalling for tens of seconds. Blocking Update on it freezes
// keystrokes, rendering and every pane in every project, which is the same
// wedge class as the 2026-06-11/12 PTY-write incidents that put every PTY
// write behind a per-pane queue. Displays failures are logged, not returned.
//
// Callers that genuinely need the display result — `quil notify test`, whose
// entire purpose is reporting the real HRESULT — use SyncNotifier instead.
type Notifier interface {
	Notify(Notification) error
	Withdraw(tag string) error
	Close() error
}

// SyncNotifier is implemented by notifiers that can also wait for a DISPLAY
// result. Never used from the TUI — see the Notifier doc for why.
type SyncNotifier interface {
	NotifySync(Notification) error
	WithdrawSync(tag string) error
}

// Options identifies which registration a process should use.
type Options struct {
	AUMID  string
	Scheme string
}

// devAUMIDSuffix marks the dev variant. Used by ShortcutBaseName so the
// filename is DERIVED from the AUMID rather than being a third independent
// string that can drift out of step with it.
const devAUMIDSuffix = ".dev"

// Variant returns the identifiers for a build.
//
// These artifacts are machine-global — a Start Menu shortcut and an HKCU class
// key — which makes them the first thing Quil writes that QUIL_HOME cannot
// redirect. Namespacing them by build variant is what lets a dev instance
// coexist with production the way the daemons already do, and is also what
// makes the feature testable in dev mode at all.
func Variant(dev bool) Options {
	if dev {
		return Options{AUMID: "artyomsv.quil" + devAUMIDSuffix, Scheme: "quil-dev"}
	}
	return Options{AUMID: "artyomsv.quil", Scheme: "quil"}
}

// ShortcutBaseName is the Start Menu filename for a variant.
//
// Derived from the AUMID rather than stored beside it: the two must never name
// different variants, and a dev build writing production's shortcut is exactly
// the failure .claude/rules/dev-environment.md exists to prevent.
func ShortcutBaseName(opts Options) string {
	if strings.HasSuffix(opts.AUMID, devAUMIDSuffix) {
		return "Quil (dev).lnk"
	}
	return "Quil.lnk"
}
