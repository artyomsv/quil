//go:build windows

package main

import (
	"syscall"
	"testing"
)

// A handle whose GetConsoleMode probe failed must be SKIPPED, not restored.
//
// This is the guard that matters most, because getting it wrong is silent and
// destructive rather than merely useless: a failed probe leaves mode at zero,
// and SetConsoleMode(handle, 0) on a real console clears every flag including
// ENABLE_PROCESSED_OUTPUT and ENABLE_VIRTUAL_TERMINAL_PROCESSING. That turns a
// cosmetic indentation bug into a console with no ANSI handling at all — worse
// than what it was called to fix.
//
// Verified by construction rather than against a live console: the test binary
// has no console to probe, which is exactly why the ok flag exists.
func TestRestoreConsoleMode_SkipsHandlesThatWereNotConsoles(t *testing.T) {
	savedModes := savedConsoleModes
	savedCall := setConsoleModeCall
	t.Cleanup(func() {
		savedConsoleModes = savedModes
		setConsoleModeCall = savedCall
	})

	// Recorded rather than merely "did not panic": the real syscall returns 0 on
	// an invalid handle and the code logs and continues, so deleting the guard
	// left the previous version of this test passing.
	var calls []savedMode
	setConsoleModeCall = func(h syscall.Handle, mode uint32) (uintptr, error) {
		calls = append(calls, savedMode{h: h, mode: mode})
		return 1, nil
	}

	savedConsoleModes = []savedMode{
		{h: syscall.Handle(^uintptr(0)), mode: 0, ok: false}, // probe failed
		{h: syscall.Handle(42), mode: 0x7, ok: true},         // real console
	}
	restoreConsoleMode()

	if len(calls) != 1 {
		t.Fatalf("SetConsoleMode called %d times, want 1 — a handle whose probe failed "+
			"was restored, and mode 0 clears ENABLE_PROCESSED_OUTPUT and VT handling", len(calls))
	}
	if calls[0].h != syscall.Handle(42) || calls[0].mode != 0x7 {
		t.Errorf("restored handle %v mode %#x, want handle 42 mode 0x7", calls[0].h, calls[0].mode)
	}
}

// saveConsoleMode records one entry per standard handle, whether or not each is
// a console. A short slice would mean a handle silently never gets restored.
func TestSaveConsoleMode_RecordsEveryStandardHandle(t *testing.T) {
	saved := savedConsoleModes
	t.Cleanup(func() { savedConsoleModes = saved })

	savedConsoleModes = nil
	saveConsoleMode()

	if got := len(savedConsoleModes); got != 3 {
		t.Errorf("recorded %d handles, want 3 (stdout, stderr, stdin)", got)
	}
}

// saveConsoleMode must be safe to call more than once without accumulating
// entries — a second call replaces the record rather than appending to it.
func TestSaveConsoleMode_IsIdempotent(t *testing.T) {
	saved := savedConsoleModes
	t.Cleanup(func() { savedConsoleModes = saved })

	saveConsoleMode()
	first := len(savedConsoleModes)
	saveConsoleMode()

	if got := len(savedConsoleModes); got != first {
		t.Errorf("second save recorded %d handles, want %d — entries are accumulating", got, first)
	}
}
