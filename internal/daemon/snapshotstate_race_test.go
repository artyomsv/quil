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
	// Verified to discriminate: deleting the writer's PluginMu pair reproduces
	// the CI failure (1 DATA RACE, test fails). Keep it that way — a race test
	// that passes with and without the guard is worse than no test.
	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	const rounds = 500
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: what handleAttach / recoverEmptyProject / ensureTabNotEmpty /
	// handleCreatePaneReq do after CreatePane has already published the pane.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			pane.PluginMu.Lock()
			pane.Type = "terminal"
			pane.PluginMu.Unlock()
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
