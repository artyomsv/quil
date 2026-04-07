package daemon_test

import (
	"testing"

	"github.com/artyomsv/quil/internal/daemon"
)

func TestSessionManagerCreateTab(t *testing.T) {
	sm := daemon.NewSessionManager(1024)
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
	sm := daemon.NewSessionManager(1024)
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
	sm := daemon.NewSessionManager(1024)
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
	sm := daemon.NewSessionManager(1024)
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
	sm := daemon.NewSessionManager(1024)
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
	sm := daemon.NewSessionManager(1024)
	_, err := sm.CreatePane("nonexistent", "")
	if err == nil {
		t.Error("expected error for nonexistent tab")
	}
}

func TestSessionManagerRestoreTab(t *testing.T) {
	sm := daemon.NewSessionManager(1024)

	tab := &daemon.Tab{
		ID:    "tab-aabbccdd",
		Name:  "Restored",
		Color: "blue",
		Panes: []string{"pane-11223344", "pane-55667788"},
	}
	panes := []*daemon.Pane{
		{ID: "pane-11223344", TabID: "tab-aabbccdd", CWD: "/home/user"},
		{ID: "pane-55667788", TabID: "tab-aabbccdd", CWD: "/tmp"},
	}

	sm.RestoreTab(tab, panes)

	tabs := sm.Tabs()
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(tabs))
	}
	if tabs[0].Name != "Restored" {
		t.Errorf("name: got %q, want %q", tabs[0].Name, "Restored")
	}

	restored := sm.Panes("tab-aabbccdd")
	if len(restored) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(restored))
	}
	if restored[0].CWD != "/home/user" {
		t.Errorf("pane[0].CWD: got %q, want %q", restored[0].CWD, "/home/user")
	}
}

func TestSessionManagerSnapshotState(t *testing.T) {
	sm := daemon.NewSessionManager(1024)
	tab1 := sm.CreateTab("tab-1")
	sm.CreatePane(tab1.ID, "/tmp")
	tab2 := sm.CreateTab("tab-2")
	sm.CreatePane(tab2.ID, "/home")

	activeTab, tabs, panesByTab := sm.SnapshotState()

	if activeTab != tab1.ID {
		t.Errorf("activeTab: got %s, want %s", activeTab, tab1.ID)
	}
	if len(tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(tabs))
	}
	if len(panesByTab[tab1.ID]) != 1 {
		t.Errorf("expected 1 pane in tab1, got %d", len(panesByTab[tab1.ID]))
	}
	if len(panesByTab[tab2.ID]) != 1 {
		t.Errorf("expected 1 pane in tab2, got %d", len(panesByTab[tab2.ID]))
	}
}

func TestSessionManagerDestroyTabUpdatesActiveTab(t *testing.T) {
	sm := daemon.NewSessionManager(1024)
	tab1 := sm.CreateTab("tab-1")
	tab2 := sm.CreateTab("tab-2")

	sm.SwitchTab(tab1.ID)
	sm.DestroyTab(tab1.ID)

	if sm.ActiveTabID() != tab2.ID {
		t.Errorf("expected tab2 to become active, got %s", sm.ActiveTabID())
	}
}
