package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestTabModel_LeavesCached: Leaves() is called ~30x per frame (tab bar,
// View loop, spinner ticks); per-call recursive allocation is pure churn.
// The cache must invalidate on tree mutation.
func TestTabModel_LeavesCached(t *testing.T) {
	tab := NewTabModel("tab-1", "Test")
	p1 := newTestPane("pane-1")
	tab.Root = &LayoutNode{Pane: p1}

	a := tab.Leaves()
	b := tab.Leaves()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("Leaves() = %d/%d panes, want 1/1", len(a), len(b))
	}
	if &a[0] != &b[0] {
		t.Error("Leaves() reallocated without tree mutation — cache not working")
	}

	tab.RemovePane("pane-1")
	if got := tab.Leaves(); len(got) != 0 {
		t.Errorf("Leaves() after RemovePane = %d panes, want 0 — stale cache", len(got))
	}
}

// TestTabModel_LeavesCached_MultiPane covers the split and non-root
// RemovePane invalidation branches (the single-pane test's RemovePane
// short-circuits to Root=nil without consulting RemoveLeaf).
func TestTabModel_LeavesCached_MultiPane(t *testing.T) {
	tab := NewTabModel("tab-1", "Test")
	p1 := newTestPane("pane-1")
	tab.Root = &LayoutNode{Pane: p1}

	// Prime the cache, then split + fill (mirrors the production flow:
	// SplitAtPane creates a placeholder, applyWorkspaceState fills it).
	if got := len(tab.Leaves()); got != 1 {
		t.Fatalf("Leaves() before split = %d panes, want 1", got)
	}
	placeholder := tab.SplitAtPane("pane-1", SplitVertical)
	if placeholder == nil {
		t.Fatal("SplitAtPane returned nil placeholder")
	}
	p2 := newTestPane("pane-2")
	placeholder.Pane = p2

	leaves := tab.Leaves()
	if len(leaves) != 2 {
		t.Fatalf("Leaves() after split-fill = %d panes, want 2 — split-path invalidation missing", len(leaves))
	}
	if leaves[0] != p1 || leaves[1] != p2 {
		t.Errorf("Leaves() after split-fill = [%s %s], want [pane-1 pane-2]", leaves[0].ID, leaves[1].ID)
	}

	// Prime the cache again, then remove the second pane through the
	// RemoveLeaf (sibling-promotion) branch.
	_ = tab.Leaves()
	tab.RemovePane("pane-2")
	leaves = tab.Leaves()
	if len(leaves) != 1 {
		t.Fatalf("Leaves() after RemovePane = %d panes, want 1 — stale cache", len(leaves))
	}
	if leaves[0] != p1 {
		t.Errorf("Leaves() after RemovePane = [%s], want [pane-1]", leaves[0].ID)
	}
}

func TestMsgTypeName_MatchesReflection(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{},
		sidebarTickMsg{},
		paneEventMsg{},
		workSpinnerTickMsg{},
	} {
		if got, want := msgTypeName(msg), fmt.Sprintf("%T", msg); got != want {
			t.Errorf("msgTypeName(%T) = %q, want %q", msg, got, want)
		}
	}
}
