package daemon

import (
	"sync"
	"testing"
)

// SnapshotState promises "a consistent view of the entire session state under a
// single RLock hold... prevents torn reads when tabs/panes are created or
// destroyed concurrently". It delivers that for projects — which are DEEP
// COPIED, with a comment saying why — and not for tabs, which are returned as
// live *Tab pointers.
//
// So every caller that reads tab.Panes after the unlock (workspaceStateFromSnapshot,
// snapshot, search, the memory report, list_tabs) races any concurrent
// CreatePane doing `tab.Panes = append(...)`. Two clients attaching at once is
// enough, because each conn is dispatched on its own goroutine: one attach
// creates the default workspace while the other builds workspace state.
//
// Caught by CI's race detector on the release build of #167, from
// TestHandleSubscribe_DispatchArmStopsPaneOutputForThatClientOnly attaching two
// clients — the first test in the package to do so concurrently. A local
// `go test -race ./...` had passed: the window is narrow enough that it needs
// load to hit, which is exactly why this reproducer drives it deliberately
// rather than relying on an incidental one.
//
// Run under -race, this fails without the fix.
func TestSnapshotState_TabPanesDoNotRaceConcurrentCreatePane(t *testing.T) {
	sm := NewSessionManager(1024)
	tab := sm.CreateTab("race")

	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: the CreatePane path an attach takes when it builds the default
	// workspace.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := sm.CreatePane(tab.ID, ""); err != nil {
				return
			}
		}
	}()

	// Reader: what every SnapshotState consumer does with the tabs it gets
	// back — read tab.Panes AFTER the lock has been released.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_, tabs, _, _, _ := sm.SnapshotState()
			for _, tb := range tabs {
				for _, pid := range tb.Panes {
					_ = pid
				}
			}
		}
	}()

	wg.Wait()
}

// The second race of this class, caught by CI on PR #175 rather than locally —
// a reminder that a green local `test-race` is evidence about one run, not a
// proof.
//
// handleAttach creates the default workspace when no tabs exist, and wrote
// `pane.Type = "terminal"` WITHOUT PluginMu. workspaceStateFromSnapshot reads
// Type under PluginMu, and says so in its own comment ("Type and CWD are
// PluginMu-protected"). CreatePane publishes the pane into sm.panes before
// returning, so the write lands on an object another connection's goroutine can
// already see.
//
// Two clients attaching at the same moment is enough, because each conn is
// dispatched on its own goroutine: one builds the default workspace while the
// other builds the workspace state it is about to be sent — the same shape as
// TestSnapshotState_TabPanesDoNotRaceConcurrentCreatePane above.
//
// Pre-existing on master; the render-coalescing branch only shifted timing
// enough to surface it. Run under -race, this fails without the PluginMu guard.
func TestPaneType_WriteDoesNotRaceSnapshotRead(t *testing.T) {
	sm := NewSessionManager(1024)
	tab := sm.CreateTab("race")

	// ONE published pane, raced on, so both goroutines are guaranteed to be on
	// the same field rather than relying on a fresh pane per iteration being
	// observed before the writer moves on.
	//
	// Verified to discriminate against the PRODUCTION guard: removing the lock
	// from setPaneType reproduces the CI failure. Keep the writer calling that
	// function — a race test with its own inlined lock pair passes whether or
	// not production has one, which is how the first version of this test came
	// to be green against the exact code it was written to pin.
	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	const rounds = 500
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: the PRODUCTION function, not a copy of it. handleAttach,
	// recoverEmptyProject and ensureTabNotEmpty all call setPaneType after
	// CreatePane has published the pane.
	//
	// Driving the real symbol is the whole point. The first version of this
	// test inlined its own PluginMu pair, so it asserted only that a guarded
	// write does not race a guarded read — true unconditionally, and green
	// against the unguarded production code it was written to pin.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			setPaneType(pane, "terminal")
		}
	}()

	// Reader: workspaceStateFromSnapshot's capture of the PluginMu-guarded set.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_, tabs, panesByTab, _, _ := sm.SnapshotState()
			for _, panes := range panesByTab {
				for _, p := range panes {
					p.PluginMu.Lock()
					_ = p.Type
					p.PluginMu.Unlock()
				}
			}
			for _, tb := range tabs {
				_ = tb.Panes
			}
		}
	}()

	wg.Wait()
}

// The same class for pane.CWD, whose window is far wider: the Type race needed
// two clients attaching at the same instant, this one fires on every `cd` in
// every pane. The TUI's OSC 7 handler sets the pane CWD, handlePaneOutput sends
// MsgUpdatePane, and handleUpdatePane wrote it with no lock while
// workspaceStateFromSnapshot, buildPaneInfos and buildPaneStatus all read it
// under PluginMu — a contract `.claude/rules/daemon-lifecycle.md` states
// explicitly.
//
// Drives the production setter, not a copy of it, for the reason spelled out on
// the Type test above.
func TestPaneCWD_WriteDoesNotRaceSnapshotRead(t *testing.T) {
	sm := NewSessionManager(1024)
	tab := sm.CreateTab("race")

	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	const rounds = 500
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: handleUpdatePane's store, reached on every OSC 7 directory change.
	// Varying lengths so a torn (pointer, length) read is reachable rather than
	// masked by every value being the same size.
	go func() {
		defer wg.Done()
		dirs := []string{"/a", "/a/much/longer/path", "/b", ""}
		for i := 0; i < rounds; i++ {
			setPaneCWD(pane, dirs[i%len(dirs)])
		}
	}()

	// Reader: the guarded capture every snapshot consumer performs.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_, _, panesByTab, _, _ := sm.SnapshotState()
			for _, panes := range panesByTab {
				for _, p := range panes {
					p.PluginMu.Lock()
					_ = p.CWD
					p.PluginMu.Unlock()
				}
			}
		}
	}()

	wg.Wait()
}
