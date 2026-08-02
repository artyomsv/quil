package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
)

func TestSidebarShowsProjectCountsAndBlockedReason(t *testing.T) {
	working := &PaneModel{ID: "pane-1"}
	working.working = true
	blocked := &PaneModel{ID: "pane-2"}
	blocked.blockedSince = time.Now()
	blocked.blockedReason = "Bash"

	tab := NewTabModel("tab-1", "AI")
	tab.Root = &LayoutNode{Left: NewLeaf(working), Right: NewLeaf(blocked), Ratio: 0.5}

	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "quil", tabs: []*TabModel{tab}}},
		sidebarOpen:  true,
		sidebarWidth: 22,
	}

	out := m.renderSidebar(20)
	if !strings.Contains(out, "quil") {
		t.Fatal("project name missing")
	}
	if !strings.Contains(out, "Bash") {
		t.Fatal("blocked reason missing — a bare ⚠ does not say what it wants")
	}
}

func TestSidebarSanitizesRemoteStrings(t *testing.T) {
	m := Model{
		projects:     []*ProjectModel{{ID: "proj-a", Name: "evil‮coffee", Dest: "gpu01"}},
		sidebarOpen:  true,
		sidebarWidth: 22,
	}
	if strings.ContainsRune(m.renderSidebar(10), '‮') {
		t.Fatal("a bidi override from a remote host reached the screen")
	}
}

func TestSidebarWidthZeroWhenClosedOrNarrow(t *testing.T) {
	if got := sidebarWidth(200, false, 22); got != 0 {
		t.Fatalf("closed = %d, want 0", got)
	}
	if got := sidebarWidth(60, true, 22); got != 0 {
		t.Fatalf("narrow terminal must auto-collapse, got %d", got)
	}
	if got := sidebarWidth(200, true, 22); got != 22 {
		t.Fatalf("width = %d, want 22", got)
	}
	// A configured width larger than the terminal must not drive
	// paneAreaWidth() negative — it reaches tab.Resize and lipgloss.Width()
	// downstream. Clamped to leave at least minTermWidth for panes.
	if got := sidebarWidth(200, true, 5000); got != 200-minTermWidth {
		t.Fatalf("oversized configured width = %d, want %d (200-minTermWidth)", got, 200-minTermWidth)
	}
}

// config can't import tui (tui already imports config), so UIConfig's
// shipped SidebarWidth default is a literal that has to be kept in sync by
// hand with defaultSidebarWidth here. This pins the two together so a future
// edit to one side shows up as a test failure instead of a silent drift.
func TestUIDefault_SidebarWidthMatchesTUIDefault(t *testing.T) {
	if got := config.Default().UI.SidebarWidth; got != defaultSidebarWidth {
		t.Fatalf("config.Default().UI.SidebarWidth = %d, want %d (defaultSidebarWidth)", got, defaultSidebarWidth)
	}
}
