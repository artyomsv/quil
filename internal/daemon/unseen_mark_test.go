package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// callUpdatePaneUnseen drives handleUpdatePane with only Unseen set, mirroring
// how the TUI reports the mark on every set and clear.
func callUpdatePaneUnseen(t *testing.T, d *Daemon, paneID string, unseen bool) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID: paneID,
		Unseen: &unseen,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdatePane(nil, msg)
}

// The "work finished while you were not looking" mark lived only in the TUI
// process, so a TUI restart dropped every green tab. The daemon now keeps a
// copy the TUI reports, in both directions: a clear must reach the pane just
// as a set does, or a restart would resurrect marks the user already looked
// at.
func TestUpdatePane_SetsAndClearsUnseen(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}})

	callUpdatePaneUnseen(t, d, "pane-1", true)
	if got := d.session.Pane("pane-1"); got == nil || !got.Unseen {
		t.Fatal("Unseen was not set")
	}
	callUpdatePaneUnseen(t, d, "pane-1", false)
	if got := d.session.Pane("pane-1"); got == nil || got.Unseen {
		t.Fatal("Unseen was not cleared — an explicit false must reach the pane")
	}
}

// handleUpdatePane is a PARTIAL update handler: every OSC 7 CWD change the
// shell emits goes through it. A plain bool would decode those as false and
// silently unmark the pane on the next `cd`.
func TestUpdatePane_PartialUpdateLeavesUnseenAlone(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{
		{ID: "pane-1", TabID: "tab-1", Type: "terminal", Unseen: true},
	})

	for _, tt := range []struct {
		name    string
		payload ipc.UpdatePanePayload
	}{
		{"rename", ipc.UpdatePanePayload{PaneID: "pane-1", Name: "renamed"}},
		{"cwd change", ipc.UpdatePanePayload{PaneID: "pane-1", CWD: "/tmp"}},
		{"pin", ipc.UpdatePanePayload{PaneID: "pane-1", PinnedAttention: boolPtr(true)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ipc.NewMessage(ipc.MsgUpdatePane, tt.payload)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			d.handleUpdatePane(nil, msg)
			if got := d.session.Pane("pane-1"); got == nil || !got.Unseen {
				t.Errorf("a %s cleared Unseen", tt.name)
			}
		})
	}
}

// An unseen update must NOT broadcast the workspace state. The TUI reports the
// mark on every completion it derives, and at attach it re-derives marks from
// the replayed event history — dozens of reports in a burst on a large
// workspace. A full workspace-state frame per report is the documented
// force-disconnect shape on the 64-slot critical queue. The TUI owns the live
// value and seeds from the daemon only once, so it needs no echo; the disk
// snapshot (debounced) is the whole point.
func TestUpdateTouchesBroadcastState_UnseenAloneDoesNot(t *testing.T) {
	t.Parallel()
	if updateTouchesBroadcastState(ipc.UpdatePanePayload{PaneID: "pane-1", Unseen: boolPtr(true)}) {
		t.Error("an unseen-only update must not broadcast the workspace state")
	}
	// The guard must not eat a real change riding in the same payload.
	if !updateTouchesBroadcastState(ipc.UpdatePanePayload{PaneID: "pane-1", Unseen: boolPtr(true), Muted: boolPtr(true)}) {
		t.Error("an unseen update beside a mute toggle must still broadcast")
	}
}

// The predicate test above pins the decision; this pins the HANDLER. Deleting
// the unseen-only diversion in handleUpdatePane leaves the predicate test green
// and brings back a full workspace-state frame per report; dropping the
// requestSnapshot inside it leaves the round-trip test green too, because that
// test calls d.snapshot() itself. Measured through a real attached client
// (countWorkspaceFrames) and the daemon's own snapshot request slot.
func TestUpdatePane_UnseenOnlyReportSkipsTheBroadcastButSchedulesTheSnapshot(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}})

	client := attachTestClient(t, sock)
	defer client.Close()
	frames := countWorkspaceFrames(client)
	waitUntil(t, "the attach broadcast to land", func() bool { return frames.Count() > 0 })
	frames.Reset()
	// Drain whatever the attach left in the (buffered-to-1) request slot so the
	// assertion below sees only what the handler scheduled.
	select {
	case <-d.snapshotCh:
	default:
	}

	callUpdatePaneUnseen(t, d, "pane-1", true)
	time.Sleep(300 * time.Millisecond)

	if n := frames.Count(); n != 0 {
		t.Errorf("an unseen-only report broadcast %d workspace_state frames, want 0", n)
	}
	if len(d.snapshotCh) != 1 {
		t.Error("the handler scheduled no snapshot, so the copy lives only in memory until the periodic ticker fires")
	}

	// Control arm: a real change riding beside the mark still broadcasts.
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: "pane-1", Unseen: boolPtr(true), Muted: boolPtr(true)})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdatePane(nil, msg)
	waitUntil(t, "the mute toggle's broadcast to land", func() bool { return frames.Count() > 0 })
}

// The headline requirement: the mark survives a daemon restart. Driven through
// DISK, like the pin's twin test, because a key the writer emits and the
// reader ignores is the same bug one step later.
func TestSnapshot_UnseenSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	marked, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	plain, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	marked.Unseen = true
	d.snapshot()

	d2 := newTestDaemonInDir(t, dir)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	got := d2.session.Pane(marked.ID)
	if got == nil {
		t.Fatal("marked pane did not survive restore")
	}
	if !got.Unseen {
		t.Error("Unseen did not survive the snapshot round trip")
	}
	// Control arm: the key is written only when true, so a reader defaulting
	// to true would mark every pane in the workspace.
	if other := d2.session.Pane(plain.ID); other == nil || other.Unseen {
		t.Error("an unmarked pane came back marked")
	}
}
