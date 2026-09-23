package daemon

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// MovePane made Pane.TabID MUTABLE, and it had been documented immutable and
// read with no lock at all from many goroutines. The 5 s memreport collector
// is one of them: paneSourceAdapter.Snapshot runs AFTER PaneSources released
// sm.mu, so holding sm.mu in the writer protects nothing there.
//
// This drives the PRODUCTION writer (the move_pane dispatch arm) against the
// PRODUCTION reader (PaneSources → Snapshot). It fails under -race against a
// plain `pane.TabID = tabID` write in MovePane, or against paneSourceAdapter
// reading `a.p.TabID` directly instead of CurrentTabID().
//
// Run under ./scripts/dev.sh test-race internal/daemon.
func TestMovePane_DoesNotRacePaneSources(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	proj := d.session.CreateProject("alpha", "/a")
	a := d.session.CreateTabInProject(proj.ID, "A")
	b := d.session.CreateTabInProject(proj.ID, "B")
	// Each tab keeps a pane of its own, so neither ever dissolves.
	for _, tab := range []*Tab{a, b} {
		if _, err := d.session.CreatePane(tab.ID, ""); err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
	}
	moving, err := d.session.CreatePane(a.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	const rounds = 500
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		dst := b.ID
		for i := 0; i < rounds; i++ {
			msg, err := ipc.NewMessage(ipc.MsgMovePane, ipc.MovePanePayload{PaneID: moving.ID, TabID: dst})
			if err != nil {
				t.Errorf("NewMessage: %v", err)
				return
			}
			d.handleMessage(nil, msg)
			if dst == b.ID {
				dst = a.ID
			} else {
				dst = b.ID
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			for _, s := range d.session.PaneSources() {
				_ = s.Snapshot().TabID
			}
		}
	}()

	wg.Wait()
}

// handleUpdateLayout wrote tab.Layout through the live *Tab pointer with no
// lock, while SnapshotState copies it under sm.mu.RLock — and MovePane now
// reads it under sm.mu for its template check. Drives the PRODUCTION writer
// (the update_layout dispatch arm) against SnapshotState; it fails under -race
// against the old `tab.Layout = payload.Layout` write.
//
// Run under ./scripts/dev.sh test-race internal/daemon.
func TestUpdateLayout_DoesNotRaceSnapshotState(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("layout")

	const rounds = 500
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			layout := json.RawMessage(fmt.Sprintf(`{"pane_id":"p%d"}`, i))
			msg, err := ipc.NewMessage(ipc.MsgUpdateLayout, ipc.UpdateLayoutPayload{TabID: tab.ID, Layout: layout})
			if err != nil {
				t.Errorf("NewMessage: %v", err)
				return
			}
			d.handleMessage(nil, msg)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_, tabs, _, _, _ := d.session.SnapshotState()
			for _, tb := range tabs {
				_ = len(tb.Layout)
			}
		}
	}()

	wg.Wait()
}
