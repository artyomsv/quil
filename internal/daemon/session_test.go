package daemon_test

import (
	"testing"

	"github.com/stukans/aethel/internal/daemon"
)

func TestSessionManagerCreateTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")

	if tab.ID == "" {
		t.Error("expected non-empty tab ID")
	}
	if tab.Name != "test-tab" {
		t.Errorf("name: got %q, want %q", tab.Name, "test-tab")
	}

	tabs := sm.Tabs()
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(tabs))
	}
}

func TestSessionManagerCreatePane(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")
	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	if pane.ID == "" {
		t.Error("expected non-empty pane ID")
	}

	panes := sm.Panes(tab.ID)
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
}

func TestSessionManagerDestroyPane(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")
	pane, _ := sm.CreatePane(tab.ID, "")

	if err := sm.DestroyPane(pane.ID); err != nil {
		t.Fatalf("DestroyPane: %v", err)
	}

	panes := sm.Panes(tab.ID)
	if len(panes) != 0 {
		t.Fatalf("expected 0 panes, got %d", len(panes))
	}
}

func TestSessionManagerDestroyTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")
	sm.CreatePane(tab.ID, "")

	if err := sm.DestroyTab(tab.ID); err != nil {
		t.Fatalf("DestroyTab: %v", err)
	}

	tabs := sm.Tabs()
	if len(tabs) != 0 {
		t.Fatalf("expected 0 tabs, got %d", len(tabs))
	}
}

func TestSessionManagerActiveTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab1 := sm.CreateTab("tab-1")
	tab2 := sm.CreateTab("tab-2")

	if sm.ActiveTabID() != tab1.ID {
		t.Error("expected first tab to be active")
	}

	sm.SwitchTab(tab2.ID)
	if sm.ActiveTabID() != tab2.ID {
		t.Errorf("expected tab2 active, got %s", sm.ActiveTabID())
	}
}

func TestSessionManagerCreatePaneInvalidTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	_, err := sm.CreatePane("nonexistent", "")
	if err == nil {
		t.Error("expected error for nonexistent tab")
	}
}

func TestSessionManagerDestroyTabUpdatesActiveTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab1 := sm.CreateTab("tab-1")
	tab2 := sm.CreateTab("tab-2")

	sm.SwitchTab(tab1.ID)
	sm.DestroyTab(tab1.ID)

	if sm.ActiveTabID() != tab2.ID {
		t.Errorf("expected tab2 to become active, got %s", sm.ActiveTabID())
	}
}
