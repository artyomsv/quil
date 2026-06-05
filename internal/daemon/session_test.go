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

// sessionTabNames returns the ordered tab names for assertions — the
// test relies on the SessionManager.Tabs() order matching the underlying
// tabOrder. t.Helper() so a failure inside the helper reports the
// caller's line, not this function's.
func sessionTabNames(t *testing.T, sm *daemon.SessionManager) []string {
	t.Helper()
	out := make([]string, 0)
	for _, tab := range sm.Tabs() {
		out = append(out, tab.Name)
	}
	return out
}

func TestSessionManagerReorderTab(t *testing.T) {
	t.Parallel()
	type step struct {
		tabName  string
		newIndex int
		want     []string
	}
	cases := []struct {
		name  string
		setup []string
		steps []step
	}{
		{
			name:  "move first to last",
			setup: []string{"A", "B", "C", "D"},
			steps: []step{{"A", 3, []string{"B", "C", "D", "A"}}},
		},
		{
			name:  "move last to first",
			setup: []string{"A", "B", "C", "D"},
			steps: []step{{"D", 0, []string{"D", "A", "B", "C"}}},
		},
		{
			name:  "move middle one step right",
			setup: []string{"A", "B", "C", "D"},
			steps: []step{{"B", 2, []string{"A", "C", "B", "D"}}},
		},
		{
			name:  "out-of-range clamps to last",
			setup: []string{"A", "B", "C"},
			steps: []step{{"A", 99, []string{"B", "C", "A"}}},
		},
		{
			name:  "negative clamps to first",
			setup: []string{"A", "B", "C"},
			steps: []step{{"C", -5, []string{"C", "A", "B"}}},
		},
		{
			name:  "noop when already in place",
			setup: []string{"A", "B", "C"},
			steps: []step{{"B", 1, []string{"A", "B", "C"}}},
		},
		{
			name:  "sequential drags slide through",
			setup: []string{"A", "B", "C", "D", "E"},
			steps: []step{
				{"A", 2, []string{"B", "C", "A", "D", "E"}},
				{"A", 4, []string{"B", "C", "D", "E", "A"}},
				{"A", 0, []string{"A", "B", "C", "D", "E"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sm := daemon.NewSessionManager(1024)
			ids := make(map[string]string)
			for _, n := range tc.setup {
				ids[n] = sm.CreateTab(n).ID
			}
			for i, s := range tc.steps {
				_ = sm.ReorderTab(ids[s.tabName], s.newIndex)
				got := sessionTabNames(t, sm)
				if len(got) != len(s.want) {
					t.Fatalf("step %d: got %v, want %v", i, got, s.want)
				}
				for j := range got {
					if got[j] != s.want[j] {
						t.Errorf("step %d: pos %d got %q want %q (full: %v)", i, j, got[j], s.want[j], got)
					}
				}
			}
		})
	}
}

func TestSessionManagerReorderTab_UnknownTabReturnsFalse(t *testing.T) {
	t.Parallel()
	sm := daemon.NewSessionManager(1024)
	sm.CreateTab("A")
	if changed := sm.ReorderTab("does-not-exist", 0); changed {
		t.Errorf("ReorderTab(unknown) = true, want false")
	}
}
