package daemon

import (
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// MoveTab is a second LOCKED writer of tab.ProjectID (MergeProjects being the
// first) — SessionManager.MoveTab writes it under sm.mu.Lock(). Two daemon
// handlers used to read the same field off the LIVE *Tab pointer with NO
// lock at all: `d.session.Tab(id).ProjectID`, in handleDestroyTab and in
// buildPaneStatus (the pane-status read path). That is exactly the shape
// TestSnapshotState_TabPanesDoNotRaceConcurrentCreatePane documents for
// tab.Panes — a locked writer and an unlocked reader of the same live
// pointer's field, on two different goroutines (one conn's move, another
// conn's status request).
//
// This drives the PRODUCTION read function, buildPaneStatus, which is what
// makes the test discriminate: it fails under -race against the old
// `Tab(id).ProjectID` read and passes against TabProjectID's RLock.
//
// Run under ./scripts/dev.sh test-race internal/daemon.
func TestMoveTab_DoesNotRaceBuildPaneStatus(t *testing.T) {
	d := New(config.Default())
	a := d.session.CreateProject("A", "/a")
	b := d.session.CreateProject("B", "/b")
	tab := d.session.CreateTabInProject(a.ID, "race")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	const rounds = 500
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: MoveTab bounces the tab between the two projects — the same
	// sm.mu-locked write a real "move to project" action takes.
	go func() {
		defer wg.Done()
		dst := b.ID
		for i := 0; i < rounds; i++ {
			d.session.MoveTab(tab.ID, dst)
			if dst == b.ID {
				dst = a.ID
			} else {
				dst = b.ID
			}
		}
	}()

	// Reader: the production status-building function, driven directly —
	// exactly what handlePaneStatusReq calls per pane.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_ = d.buildPaneStatus(pane)
		}
	}()

	wg.Wait()
}
