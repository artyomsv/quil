package tui

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestMain shortens the client-side timers that no test asserts on.
//
// tea.Tick's Cmd blocks for its full duration, and runCmd walks a tea.Batch's
// children synchronously, so a test that issues a browse or git-discovery
// request pays the whole production timeout — eight seconds each, linear in
// the number of such tests, and multiplied again under -race. Shortening it
// here costs nothing: the tick still fires and still produces its *TimeoutMsg,
// runCmd discards that message rather than dispatching it, and the tests that
// DO exercise a timeout path call applyBrowseTimeout / applyGitScanTimeout
// directly with an explicit key instead of waiting for a timer.
//
// Done once, here, rather than per test: these tests use t.Parallel(), so
// assigning to the package vars mid-run would be a data race that -race would
// (correctly) fail on.
//
// It also makes production-home isolation the DEFAULT for this package's
// tests, rather than a per-test opt-in — see ensureIsolatedQuilHome's own
// doc comment for why.
func TestMain(m *testing.M) {
	browseTimeout = 10 * time.Millisecond
	gitScanTimeout = 10 * time.Millisecond
	kubeScanTimeout = 10 * time.Millisecond
	recentScanTimeout = 10 * time.Millisecond

	cleanupQuilHome := ensureIsolatedQuilHome()
	code := m.Run()
	cleanupQuilHome() // NOT deferred: os.Exit below skips every deferred call.
	os.Exit(code)
}

// ensureIsolatedQuilHome makes production-home isolation the default for
// package tui's tests instead of a per-test opt-in.
//
// applyWorkspaceState writes and disconnectDest removes files under
// config.QuilDir() (internal/tui/remoteprojects.go, dialdest.go), which
// resolves to the developer's own ~/.quil the moment QUIL_HOME is unset. Every
// test that reaches either path is safe today only because it happens to pass
// dest == "" — a property of the test's ARGUMENTS, not of the code — and this
// branch has already shipped that exact defect twice. A test exercising a
// non-empty dest without its own t.Setenv("QUIL_HOME", ...) would silently
// read or write the real production directory.
//
// Existing per-test t.Setenv("QUIL_HOME", ...) calls are UNCHANGED by this:
// t.Setenv overrides the process-wide value for the duration of that test and
// restores it — to what this function set, not to "unset" — afterward. This
// is only the floor under everything else, for the tests that assert nothing
// about QUIL_HOME at all.
//
// Returns a cleanup func rather than using t.TempDir()/t.Cleanup(): TestMain
// receives a *testing.M, not a *testing.T, so neither is available here.
func ensureIsolatedQuilHome() func() {
	if os.Getenv("QUIL_HOME") != "" {
		return func() {}
	}
	dir, err := os.MkdirTemp("", "quil-tui-test-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: creating isolated QUIL_HOME: %v\n", err)
		os.Exit(1)
	}
	os.Setenv("QUIL_HOME", dir)
	return func() { os.RemoveAll(dir) }
}
