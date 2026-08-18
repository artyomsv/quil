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
