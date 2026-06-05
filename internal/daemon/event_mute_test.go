package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// callUpdatePaneMute drives handleUpdatePane with just the Muted field set,
// mirroring how the TUI's toggleActivePaneMute sends the message.
func callUpdatePaneMute(d *Daemon, paneID string, muted bool) error {
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
		PaneID: paneID,
		Muted:  &muted,
	})
	if err != nil {
		return err
	}
	d.handleUpdatePane(msg)
	return nil
}

// TestEmitEvent_MutedPaneDropsEvent verifies that events sourced from a muted
// pane are neither queued nor broadcast. This is the contract behind the
// Alt+M keybinding — muting must be a *signal* mute, not a "hide in UI" mute.
func TestEmitEvent_MutedPaneDropsEvent(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-loud", "pane-quiet"}}
	panes := []*Pane{
		{ID: "pane-loud", TabID: "tab-1", Type: "terminal"},
		{ID: "pane-quiet", TabID: "tab-1", Type: "terminal", Muted: true},
	}
	d.session.RestoreTab(tab, panes)

	d.emitEvent(PaneEvent{ID: "evt-1", PaneID: "pane-loud", Type: "output_idle", Title: "Output idle"})
	d.emitEvent(PaneEvent{ID: "evt-2", PaneID: "pane-quiet", Type: "output_idle", Title: "Output idle"})

	events := d.events.Events()
	if len(events) != 1 {
		t.Fatalf("queue length: got %d, want 1 (muted pane should not be queued)", len(events))
	}
	if events[0].PaneID != "pane-loud" {
		t.Errorf("only non-muted event should survive: got pane %q", events[0].PaneID)
	}
}

// TestEmitEvent_UnknownPaneStillEmits guards against an over-aggressive filter
// — events whose PaneID does not resolve to a live pane (e.g. a synthetic
// daemon-level event) must still be queued.
func TestEmitEvent_UnknownPaneStillEmits(t *testing.T) {
	d := New(config.Default())
	d.emitEvent(PaneEvent{ID: "evt-orphan", PaneID: "pane-does-not-exist", Type: "bell"})
	if d.events.Count() != 1 {
		t.Errorf("orphan event should still queue: got %d, want 1", d.events.Count())
	}
}

// TestEmitEvent_EmptyPaneIDStillEmits ensures the mute filter does not gate
// daemon-level events that carry no PaneID at all.
func TestEmitEvent_EmptyPaneIDStillEmits(t *testing.T) {
	d := New(config.Default())
	d.emitEvent(PaneEvent{ID: "evt-global", Type: "info"})
	if d.events.Count() != 1 {
		t.Errorf("paneless event should still queue: got %d, want 1", d.events.Count())
	}
}

// TestHandleUpdatePane_MutedFieldToggle drives MsgUpdatePane through the
// daemon's handler and asserts the Muted bit flips. Demonstrates the
// pointer-tristate contract: Name="" leaves Name alone, Muted=&true sets it.
func TestHandleUpdatePane_MutedFieldToggle(t *testing.T) {
	d := New(config.Default())
	tab := &Tab{ID: "tab-1", Name: "test", Panes: []string{"pane-1"}}
	panes := []*Pane{
		{ID: "pane-1", TabID: "tab-1", Name: "originalName"},
	}
	d.session.RestoreTab(tab, panes)

	if panes[0].Muted {
		t.Fatal("precondition: pane should start unmuted")
	}

	if err := callUpdatePaneMute(d, "pane-1", true); err != nil {
		t.Fatalf("update to muted=true: %v", err)
	}
	if !panes[0].Muted {
		t.Errorf("after update: Muted should be true")
	}
	if panes[0].Name != "originalName" {
		t.Errorf("Name should be preserved when only Muted is updated: got %q", panes[0].Name)
	}

	if err := callUpdatePaneMute(d, "pane-1", false); err != nil {
		t.Fatalf("update to muted=false: %v", err)
	}
	if panes[0].Muted {
		t.Errorf("after second update: Muted should be false")
	}
}
