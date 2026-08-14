package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/notify"
)

// notifyVariant picks the registration namespace for THIS binary.
//
// buildDevMode is the same ldflag that redirects QUIL_HOME — but the toast
// artifacts live OUTSIDE QUIL_HOME (a Start Menu shortcut and an HKCU class
// key), so they are the one thing dev isolation does not get for free.
//
// Deliberately not derived from whether QUIL_HOME is set: a production binary
// run against a custom QUIL_HOME is still the production registration, and
// resolving it that way would have it read a dev AUMID nothing ever wrote.
func notifyVariant() notify.Options { return notify.Variant(buildDevMode == "true") }

func handleNotify() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: quil notify [setup|status]   (setup accepts --remove)")
		os.Exit(1)
	}
	opts := notifyVariant()
	switch os.Args[2] {
	case "setup":
		if len(os.Args) > 3 && os.Args[3] == "--remove" {
			runNotifyRemove(opts)
			return
		}
		runNotifySetup(opts)
	case "status":
		runNotifyStatus(opts)
	case "test":
		runNotifyTest(opts)
	default:
		fmt.Fprintf(os.Stderr, "unknown notify command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func refuseUnsupported(err error) {
	if errors.Is(err, notify.ErrUnsupported) {
		fmt.Fprintf(os.Stderr, "desktop notifications are Windows-only (this is %s)\n", runtime.GOOS)
		os.Exit(1)
	}
}

func runNotifySetup(opts notify.Options) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve this executable: %v\n", err)
		os.Exit(1)
	}
	written, err := notify.Setup(opts, exe)
	if err != nil {
		refuseUnsupported(err)
		fmt.Fprintf(os.Stderr, "notify setup failed: %v\n", err)
		os.Exit(1)
	}

	// Print exactly what was written and how to undo it. These are the only two
	// things Quil puts outside QUIL_HOME, so saying so is the price of writing
	// them at all.
	fmt.Printf("Registered desktop notifications for %s.\n\n", opts.Scheme)
	for _, w := range written {
		fmt.Printf("  %s\n", w)
	}

	// Show and immediately withdraw a toast IN THIS PROCESS, before claiming
	// success. Setup that reports success without having displayed anything
	// cannot tell the user whether it worked.
	verified := verifySetup(opts)
	fmt.Printf("\nEnabled by default in config.toml:\n\n")
	fmt.Printf("  [notification.desktop]\n  enabled = true\n\n")
	if verified {
		fmt.Printf("Verified: a test toast was displayed and withdrawn.\n\n")
	} else {
		fmt.Printf("WARNING: the verification toast could not be displayed. Check\n")
		fmt.Printf("Settings > System > Notifications, then:  quil notify test\n\n")
	}
	fmt.Printf("Undo with:  quil notify setup --remove\n")
}

// verifySetup shows and withdraws a canary toast in the setup process itself.
// Returns whether it displayed. Never fatal — a failure is reported, not an
// error, because the artifacts are written either way.
func verifySetup(opts notify.Options) bool {
	n, err := notify.New(opts)
	if err != nil {
		return false
	}
	defer n.Close()
	sync, ok := n.(notify.SyncNotifier)
	if !ok {
		return false
	}
	const tag = "pane-00000000"
	if err := sync.NotifySync(notify.Notification{
		Title:  "Quil",
		Body:   "TEST-CANARY — desktop notifications registered",
		Tag:    tag,
		Launch: notify.BuildActivateURI(opts.Scheme, os.Getpid(), tag),
	}); err != nil {
		return false
	}
	_ = sync.WithdrawSync(tag)
	return true
}

func runNotifyRemove(opts notify.Options) {
	removed, err := notify.Remove(opts)
	if err != nil {
		refuseUnsupported(err)
		fmt.Fprintf(os.Stderr, "notify setup --remove failed: %v\n", err)
		os.Exit(1)
	}
	if len(removed) == 0 {
		fmt.Printf("Nothing to remove — %s was not registered.\n", opts.Scheme)
		return
	}
	fmt.Printf("Removed desktop notification registration for %s.\n\n", opts.Scheme)
	for _, r := range removed {
		fmt.Printf("  %s\n", r)
	}
}

// runNotifyStatus is the diagnosis path for "why don't I see toasts".
//
// It exists as its own verb rather than as output from setup because the
// commonest failure has nothing to do with setup: the config default is
// enabled, so an unregistered machine is the normal first-run state and the
// user needs a way to ask what is missing.
func runNotifyStatus(opts notify.Options) {
	if runtime.GOOS != "windows" {
		fmt.Printf("Desktop notifications: unsupported on %s\n", runtime.GOOS)
		return
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		cfg = config.Default()
	}
	d := cfg.Notification.Desktop

	registered, regErr := notify.Registered(opts)
	shortcut, _ := notify.ShortcutPath(opts)

	fmt.Printf("Variant:      %s (AUMID %s)\n", opts.Scheme, opts.AUMID)
	fmt.Printf("Shortcut:     %s\n", shortcut)
	switch {
	case regErr != nil:
		fmt.Printf("Registered:   unknown (%v)\n", regErr)
	case registered:
		fmt.Printf("Registered:   yes\n")
	default:
		fmt.Printf("Registered:   NO — run 'quil notify setup'\n")
	}
	fmt.Printf("Enabled:      %v\n", d.Enabled)
	fmt.Printf("  blocked:    %v\n", d.Blocked)
	fmt.Printf("  done:       %v\n", d.Done)
	fmt.Printf("  cooldown:   %s\n", d.CooldownDuration())

	fmt.Printf("\nToasts fire for any pane you are not looking at — another tab, another\n")
	fmt.Printf("project, or another application. The pane on screen never toasts.\n")
}

// runNotifyTest sends one canary toast.
//
// This is the ONLY way to verify the whole path, and the reason is structural:
// Windows resolves an unpackaged app's AUMID by matching the running PROCESS to
// a Start Menu shortcut whose target is that process. A `go test` binary can
// never satisfy that — the shortcut points at quil.exe — so no unit test can
// cover it. Running here, inside the binary the shortcut names, is the only
// place the association holds.
//
// It doubles as the user-facing answer to "is this actually working?", which is
// why it prints what it did rather than exiting quietly.
func runNotifyTest(opts notify.Options) {
	if runtime.GOOS != "windows" {
		fmt.Fprintf(os.Stderr, "desktop notifications are Windows-only (this is %s)\n", runtime.GOOS)
		os.Exit(1)
	}
	// No LockOSThread here: the notifier owns its own COM thread (see
	// winNotifier.serve), so pinning this one would be misleading noise.
	n, err := notify.New(opts)
	if err != nil {
		refuseUnsupported(err)
		fmt.Fprintf(os.Stderr, "cannot send a test toast: %v\n", err)
		if errors.Is(err, notify.ErrNotRegistered) {
			fmt.Fprintln(os.Stderr, "\nRun 'quil notify setup' first.")
		}
		os.Exit(1)
	}
	defer n.Close()

	// The SYNCHRONOUS path, deliberately. Notifier.Notify queues and reports
	// only whether it could queue — correct for the TUI, useless here, where
	// reporting the real HRESULT is the entire point of the command.
	sync, ok := n.(notify.SyncNotifier)
	if !ok {
		fmt.Fprintln(os.Stderr, "this build cannot send a synchronous test toast")
		os.Exit(1)
	}

	// Self-labelling, and tagged with the all-zero pane id no daemon allocates,
	// so a toast that lingers cannot be mistaken for a real attention event.
	const tag = "pane-00000000"

	if len(os.Args) > 3 && os.Args[3] == "--clear" {
		if err := sync.WithdrawSync(tag); err != nil {
			fmt.Fprintf(os.Stderr, "could not clear the test toast: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Cleared the test toast.")
		return
	}

	if err := sync.NotifySync(notify.Notification{
		Title:  "Quil",
		Body:   "TEST-CANARY — desktop notifications are working",
		Tag:    tag,
		Launch: notify.BuildActivateURI(opts.Scheme, os.Getpid(), tag),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "test toast failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "\nCheck Settings > System > Notifications — notifications may be")
		fmt.Fprintln(os.Stderr, "turned off for this app, for your account, or by group policy.")
		os.Exit(1)
	}
	fmt.Println("Sent a test toast. It should be visible now, and in Action Center.")
	fmt.Println("Clicking it does nothing unless a TUI with this PID is running — that is expected.")
	fmt.Printf("\nRemove it with:  quil notify test --clear\n")
}

// handleActivate is the quil:// protocol handler, invoked by Windows when the
// user clicks a toast.
//
// It does exactly one thing: parse, validate, forward. There is deliberately no
// path from here to spawning a pane, sending input, or running a command —
// registering a URI scheme makes this reachable by any local process and,
// behind a browser confirmation, by a web page. The ceiling is "the cursor
// moves to a pane the user already owns", and that ceiling is a design
// constraint rather than an implementation detail.
func handleActivate() {
	if len(os.Args) < 3 {
		os.Exit(1)
	}
	opts := notifyVariant()
	pid, paneID, err := notify.ParseActivateURI(opts.Scheme, os.Args[2])
	if err != nil {
		os.Exit(1)
	}
	// A TUI that exited between the toast and the click leaves no listener.
	// Exit 0 silently: a stale toast must never pop an error window.
	if err := notify.SendActivate(pid, paneID); err != nil {
		return
	}

	// Then bring the terminal forward. Routing without raising means the user
	// clicks a toast, nothing appears to happen, and they alt-tab to discover
	// Quil moved — which reads as the click not working.
	//
	// AFTER SendActivate, not before: the jump should already be applied by the
	// time the window appears, so the user sees the right pane rather than
	// watching it switch. Best-effort — a failure leaves them exactly where
	// they were.
	if err := notify.RaiseWindowFor(pid); err != nil {
		logActivate("raise failed for pane %s: %v", paneID, err)
		return
	}
	logActivate("raised pid %d for pane %s", pid, paneID)
}

// maxActivateLogBytes caps the activation log. It is a diagnostic buffer, not a
// history: one line per toast click, and the interesting one is always the last.
const maxActivateLogBytes = 64 << 10

// logActivate records what a toast click did.
//
// This process is spawned by Windows, lives for milliseconds, and has no
// terminal, no logger and no daemon connection — so until now it was the one
// component in the feature that could fail with no trace anywhere. Three
// separate bugs in this path were diagnosed by correlating timestamps in other
// processes' logs by hand, which is how long a silent path stays silent.
//
// Deliberately best-effort and completely silent on failure: a diagnostic that
// can break an activation is worse than no diagnostic.
func logActivate(format string, args ...any) {
	dir := config.QuilDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "notify-activate.log")
	// Truncate rather than rotate. Rotation implies someone will read the old
	// file, and nobody will — the failure being diagnosed is always the click
	// that just happened.
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxActivateLogBytes {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
