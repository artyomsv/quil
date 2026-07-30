package tui

import (
	"os"
	"testing"
	"time"
)

// TestMain shortens the client-side timers that no test asserts on.
//
// tea.Tick's Cmd blocks for its full duration, and runCmd walks a tea.Batch's
// children synchronously, so a test that issues a browse request pays the whole
// production browseTimeout — eight seconds each, linear in the number of such
// tests, and multiplied again under -race. Shortening it here costs nothing:
// the tick still fires and still produces its browseTimeoutMsg, runCmd
// discards that message rather than dispatching it, and the tests that DO
// exercise the timeout path call applyBrowseTimeout directly with an explicit
// (path, child) instead of waiting for a timer.
//
// Done once, here, rather than per test: these tests use t.Parallel(), so
// assigning to the package var mid-run would be a data race that -race would
// (correctly) fail on.
func TestMain(m *testing.M) {
	browseTimeout = 10 * time.Millisecond
	os.Exit(m.Run())
}
