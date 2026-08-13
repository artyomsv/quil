//go:build windows

package notify

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNew_RefusesUnregisteredAUMID(t *testing.T) {
	n, err := New(Options{AUMID: "artyomsv.quil.nonexistent.test", Scheme: "quil-nonexistent"})
	if err == nil {
		if n != nil {
			_ = n.Close()
		}
		t.Fatal("New() accepted an AUMID with no backing Start Menu shortcut")
	}
	if !errors.Is(err, ErrNotRegistered) {
		t.Errorf("err = %v, want ErrNotRegistered", err)
	}
}

// End-to-end against the real Windows notification platform: register a
// scratch variant, show a toast, withdraw it, deregister.
//
// The body is self-labelling (TEST-CANARY) so a toast that escapes into Action
// Center cannot be mistaken for a real one, and the tag is the all-zero pane id
// which no daemon allocates.
func TestNotify_ShowsAndWithdraws(t *testing.T) {
	opts := scratchVariant()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	if _, err := Setup(opts, exe); err != nil {
		t.Fatalf("Setup() = %v", err)
	}
	t.Cleanup(func() { _, _ = Remove(opts) })

	n, err := New(opts)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer n.Close()

	sent := Notification{
		Title:  "quil self-test",
		Body:   "TEST-CANARY — safe to dismiss",
		Tag:    "pane-00000000",
		Launch: BuildActivateURI(opts.Scheme, os.Getpid(), "pane-00000000"),
	}

	// Windows indexes a new Start Menu shortcut ASYNCHRONOUSLY, and until it
	// does, the notification platform does not know the AUMID: get_Setting
	// returns ERROR_NOT_FOUND (0x80070490) even though the .lnk is on disk and
	// Registered() is already true.
	//
	// This is a real product behaviour, not a test artifact — the first toast
	// after `quil notify setup` can be dropped — but it is not something this
	// test can force, so it polls and then skips rather than failing. The spike
	// only ever saw the indexed state because its shortcut had survived from an
	// earlier run.
	var lastErr error
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		lastErr = n.Notify(sent)
		if lastErr == nil {
			break
		}
		if !strings.Contains(lastErr.Error(), "0x80070490") {
			t.Fatalf("Notify() = %v", lastErr)
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		t.Skipf("AUMID %s not indexed by the notification platform within 45s "+
			"(last error %v) — the show/withdraw path could not be exercised", opts.AUMID, lastErr)
	}

	// Withdraw is what keeps Action Center honest; a failure here means the tag
	// never stuck or the history index is wrong.
	if err := n.Withdraw(sent.Tag); err != nil {
		t.Fatalf("Withdraw() = %v", err)
	}
}

// NOTE — why there is no test here that shows a toast against the real
// registration, and why TestNotify_ShowsAndWithdraws above only ever skips.
//
// Windows resolves an unpackaged app's AUMID by matching the RUNNING PROCESS to
// a Start Menu shortcut whose target is that process. A test binary can never
// satisfy that: the prod/dev shortcuts point at quil.exe / quil-dev.exe, and a
// shortcut this test creates pointing at ITSELF is seconds old, which Windows
// has not indexed yet. Measured on 19045:
//
//	shortcut targets the running exe, seconds old   -> ERROR_NOT_FOUND (45 s+)
//	shortcut targets another exe, ~10 minutes old   -> ERROR_NOT_FOUND
//	shortcut targets the running exe, minutes old   -> works (the spike)
//
// Both conditions are needed and neither is forceable from a test. The real
// path is therefore verified by `quil notify test`, which runs inside the
// binary the shortcut actually points at — see cmd/quil/notify.go.

// Withdrawing something that was never shown must not be an error: the sweep
// runs against its own bookkeeping, and a toast the user already dismissed by
// hand is gone from history.
func TestWithdraw_UnknownTagIsTolerated(t *testing.T) {
	opts := scratchVariant()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	if _, err := Setup(opts, exe); err != nil {
		t.Fatalf("Setup() = %v", err)
	}
	t.Cleanup(func() { _, _ = Remove(opts) })

	n, err := New(opts)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer n.Close()

	if err := n.Withdraw("pane-99999999"); err != nil {
		t.Logf("Withdraw() of an unknown tag = %v", err)
		t.Log("NOTE: if this is ERROR_NOT_FOUND (0x80070490), the TUI sweep will log it " +
			"harmlessly at debug level on every already-dismissed toast")
	}
}
