package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// modelForActivePaneTest builds a minimal Model with one tab containing one
// active pane. Enough surface area to exercise paneEventMsg dispatch.
func modelForActivePaneTest(activePaneID string) Model {
	cfg := config.Default()
	tab := NewTabModel("tab-1", "test")
	pane := NewPaneModel(activePaneID, 1024)
	tab.Root = NewLeaf(pane)
	tab.ActivePane = activePaneID
	return Model{
		client:        &fakeSender{},
		tabs:          []*TabModel{tab},
		activeTab:     0,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
	}
}

func TestPaneEvent_OutputIdleOnActivePane_Suppressed(t *testing.T) {
	m := modelForActivePaneTest("pane-active")
	idle := paneEventMsg(ipc.PaneEventPayload{
		ID:     "evt-1",
		PaneID: "pane-active",
		Type:   "output_idle",
		Title:  "Output idle",
	})
	next, _ := m.Update(idle)
	got := next.(Model).notifications.Count()
	if got != 0 {
		t.Errorf("idle event on active pane should be suppressed; queue=%d, want 0", got)
	}
}

func TestPaneEvent_OutputIdleOnBackgroundPane_Queued(t *testing.T) {
	m := modelForActivePaneTest("pane-active")
	idle := paneEventMsg(ipc.PaneEventPayload{
		ID:     "evt-1",
		PaneID: "pane-background",
		Type:   "output_idle",
		Title:  "Output idle",
	})
	next, _ := m.Update(idle)
	got := next.(Model).notifications.Count()
	if got != 1 {
		t.Errorf("idle event on background pane should queue; queue=%d, want 1", got)
	}
}

func TestPaneEvent_ProcessExitOnActivePane_StillQueued(t *testing.T) {
	// Process exits, bells, and command completions are transient state
	// changes — they belong in the sidebar even when the user is looking at
	// the pane (the sidebar acts as a session log they can scroll back to).
	m := modelForActivePaneTest("pane-active")
	exit := paneEventMsg(ipc.PaneEventPayload{
		ID:     "evt-1",
		PaneID: "pane-active",
		Type:   "process_exit",
		Title:  "Process exited (code 0)",
	})
	next, _ := m.Update(exit)
	got := next.(Model).notifications.Count()
	if got != 1 {
		t.Errorf("process_exit event must always queue (even on active pane); queue=%d, want 1", got)
	}
}

func TestPaneEvent_BellOnActivePane_StillQueued(t *testing.T) {
	m := modelForActivePaneTest("pane-active")
	bell := paneEventMsg(ipc.PaneEventPayload{
		ID:     "evt-1",
		PaneID: "pane-active",
		Type:   "bell",
		Title:  "Attention",
	})
	next, _ := m.Update(bell)
	got := next.(Model).notifications.Count()
	if got != 1 {
		t.Errorf("bell event must always queue (even on active pane); queue=%d, want 1", got)
	}
}

func TestIsActivePane_EmptyPaneID(t *testing.T) {
	m := modelForActivePaneTest("pane-active")
	if m.isActivePane("") {
		t.Errorf("empty paneID must not match")
	}
}

func TestIsActivePane_NoActiveTab(t *testing.T) {
	cfg := config.Default()
	m := Model{
		client:        &fakeSender{},
		tabs:          nil,
		activeTab:     0,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
	}
	if m.isActivePane("anything") {
		t.Errorf("no active tab: must return false, not panic")
	}
}
