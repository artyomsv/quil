package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// callUpdatePanePinned drives handleUpdatePane with only PinnedAttention set,
// mirroring how the TUI's context menu sends the message.
func callUpdatePanePinned(t *testing.T, d *Daemon, paneID string, pinned bool) {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID:          paneID,
		PinnedAttention: &pinned,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleUpdatePane(nil, msg)
}

// TestUpdatePane_SetsAndClearsPinnedAttention pins the toggle in both
// directions. Clearing is the direction that needs the test: the payload field
// is a *bool precisely so an explicit false is distinguishable from an absent
// one, and a plain bool would make "Unmark attention" unsendable.
func TestUpdatePane_SetsAndClearsPinnedAttention(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}})

	callUpdatePanePinned(t, d, "pane-1", true)
	if got := d.session.Pane("pane-1"); got == nil || !got.PinnedAttention {
		t.Fatal("PinnedAttention was not set")
	}
	callUpdatePanePinned(t, d, "pane-1", false)
	if got := d.session.Pane("pane-1"); got == nil || got.PinnedAttention {
		t.Fatal("PinnedAttention was not cleared — an explicit false must reach the pane")
	}
}

// TestUpdatePane_PartialUpdateLeavesThePinAlone is what the *bool tri-state
// buys. handleUpdatePane is a PARTIAL update handler: the TUI sends it for a
// rename and, far more often, for every OSC 7 CWD change the shell emits. A
// plain bool would decode those as `false` and silently unmark the pane —
// which, for a mark whose whole purpose is to survive until the user clears
// it, is the one failure that makes the feature worthless.
func TestUpdatePane_PartialUpdateLeavesThePinAlone(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	d.session.RestoreTab(tab, []*Pane{
		{ID: "pane-1", TabID: "tab-1", Type: "terminal", PinnedAttention: true},
	})

	for _, tt := range []struct {
		name    string
		payload ipc.UpdatePanePayload
	}{
		{"rename", ipc.UpdatePanePayload{PaneID: "pane-1", Name: "renamed"}},
		{"cwd change", ipc.UpdatePanePayload{PaneID: "pane-1", CWD: "/tmp"}},
		{"mute toggle", ipc.UpdatePanePayload{PaneID: "pane-1", Muted: boolPtr(true)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ipc.NewMessage(ipc.MsgUpdatePane, tt.payload)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			d.handleUpdatePane(nil, msg)
			if got := d.session.Pane("pane-1"); got == nil || !got.PinnedAttention {
				t.Errorf("a %s cleared PinnedAttention", tt.name)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestSnapshot_PinnedAttentionSurvivesTheRoundTrip is the headline requirement.
// The mark is set by hand and re-derived by nothing — no hook edge will ever
// set it again — so a mark lost at restart is a mark the user has to remember
// on their own, which is what having it is supposed to replace.
//
// Driven through DISK rather than by inspecting the built state, for the reason
// TestSnapshot_WorktreeOwnershipSurvivesTheRoundTrip gives: a key the writer
// emits and the reader ignores is the same bug one step later, and only the
// round trip catches it.
func TestSnapshot_PinnedAttentionSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pinned, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	plain, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pinned.PinnedAttention = true
	d.snapshot()

	d2 := newTestDaemonInDir(t, dir)
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	got := d2.session.Pane(pinned.ID)
	if got == nil {
		t.Fatal("pinned pane did not survive restore")
	}
	if !got.PinnedAttention {
		t.Error("PinnedAttention did not survive the snapshot round trip")
	}
	// The control arm. The key is written only when true (the omit-if-false
	// idiom every flag in that block uses), so a reader that defaulted to true
	// — or a writer that emitted the key unconditionally — would mark every
	// pane in the workspace, and the assertion above alone would not notice.
	if other := d2.session.Pane(plain.ID); other == nil || other.PinnedAttention {
		t.Error("an unpinned pane came back pinned")
	}
}

// TestWorkspaceState_PinnedAttentionFlip_NoRace mirrors
// TestWorkspaceState_OverlayFlip_NoRace for the new field.
//
// The read at the `pinned_attention` write sits inside the same PluginMu span
// that already guards Overlay, so the Overlay test incidentally exercises a
// concurrent READ of this field today — but nothing concurrently WRITES it the
// way handleUpdatePane does. That is the absence of a bug the existing tests
// happen to be positioned to catch rather than coverage of this field, and a
// future edit that moves the write out of the lock (or the read out of the
// span) would leave every current test green.
func TestWorkspaceState_PinnedAttentionFlip_NoRace(t *testing.T) {
	d := New(config.Default())

	tab := &Tab{ID: "tab-pinpin", Name: "pin", Panes: []string{"pane-beefbeef"}}
	pane := &Pane{ID: "pane-beefbeef", TabID: "tab-pinpin", CWD: "/tmp"}
	tabs := []*Tab{tab}
	panesByTab := map[string][]*Pane{tab.ID: {pane}}

	const iters = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iters; i++ {
			pane.PluginMu.Lock()
			pane.PinnedAttention = i%2 == 0
			pane.PluginMu.Unlock()
		}
	}()

	for i := 0; i < iters; i++ {
		_ = d.workspaceStateFromSnapshot(tab.ID, tabs, panesByTab, nil, "", true)
	}
	<-done
}

// TestSnapshot_PinnedAttentionUsesTheWireKey pins the DAEMON half of the wire
// contract. The round-trip above proves this package's writer and reader agree
// with each other — they are the same file — but the same map is also the
// BROADCAST the TUI parses, and a rename here would leave that round trip green
// while the mark silently stopped appearing in the client.
//
// The literal is spelled out rather than shared through a constant, which would
// make both sides agree by construction and test nothing. Its twin is
// TestParseWorkspaceState_ReadsThePinWireKey in internal/tui.
func TestSnapshot_PinnedAttentionUsesTheWireKey(t *testing.T) {
	dir := t.TempDir()
	d := newTestDaemonInDir(t, dir)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PinnedAttention = true

	// includeOverlays=true is the BROADCAST shape — the one the TUI parses.
	// The flag must not gate this key: the pin has to reach both disk and wire,
	// which is why it is written outside that block.
	for _, includeOverlays := range []bool{false, true} {
		activeTab, tabs, panesByTab, projects, activeProject := d.session.SnapshotState()
		state := d.workspaceStateFromSnapshot(activeTab, tabs, panesByTab, projects, activeProject, includeOverlays)
		panes, ok := state["panes"].([]map[string]any)
		if !ok {
			t.Fatalf("includeOverlays=%v: panes is %T, want []map[string]any", includeOverlays, state["panes"])
		}
		var found bool
		for _, pd := range panes {
			if pd["id"] != pane.ID {
				continue
			}
			found = true
			if pd["pinned_attention"] != true {
				t.Errorf("includeOverlays=%v: pane data = %v, want pinned_attention:true — "+
					"the TUI parser looks for exactly that key", includeOverlays, pd)
			}
		}
		if !found {
			t.Errorf("includeOverlays=%v: the pinned pane is missing from the state", includeOverlays)
		}
	}
}
