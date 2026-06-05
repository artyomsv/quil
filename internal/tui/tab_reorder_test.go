package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// newModelForTest mirrors NewModel's invariants for fields the tests
// touch: tabDragFromIdx must start at -1 (literal zero means "tab 0 is
// being dragged" the moment a MouseMotionMsg arrives at Y=0), and
// cfg is needed by handlers that read keybindings. Other fields the
// production NewModel sets (mcpHighlights, notifications, ...) are left
// zero because the local tests don't exercise them.
func newModelForTest(names []string, activeIdx int) Model {
	m := Model{
		cfg:            config.Default(),
		tabDragFromIdx: -1,
	}
	for _, n := range names {
		m.tabs = append(m.tabs, NewTabModel(n, n))
	}
	m.activeTab = activeIdx
	return m
}

func tabNames(t *testing.T, m Model) []string {
	t.Helper()
	out := make([]string, 0, len(m.tabs))
	for _, tab := range m.tabs {
		out = append(out, tab.Name)
	}
	return out
}

func TestModel_MoveTab(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		setup       []string
		activeIdx   int
		from, to    int
		wantChanged bool
		want        []string
		wantActive  int
	}{
		{"forward middle", []string{"A", "B", "C", "D"}, 1, 1, 2, true, []string{"A", "C", "B", "D"}, 2},
		{"backward end-to-start", []string{"A", "B", "C", "D"}, 3, 3, 0, true, []string{"D", "A", "B", "C"}, 0},
		{"forward start-to-end", []string{"A", "B", "C", "D"}, 0, 0, 3, true, []string{"B", "C", "D", "A"}, 3},
		{"noop same idx", []string{"A", "B", "C"}, 1, 1, 1, false, []string{"A", "B", "C"}, 1},
		{"from out of range", []string{"A", "B"}, 1, 99, 0, false, []string{"A", "B"}, 1},
		{"to out of range", []string{"A", "B"}, 1, 0, 99, false, []string{"A", "B"}, 1},
		{"negative from", []string{"A", "B"}, 1, -1, 0, false, []string{"A", "B"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newModelForTest(tc.setup, tc.activeIdx)
			changed := m.moveTab(tc.from, tc.to)
			if changed != tc.wantChanged {
				t.Errorf("moveTab(%d, %d) = %v, want %v", tc.from, tc.to, changed, tc.wantChanged)
			}
			got := tabNames(t, m)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("after move: got %v, want %v", got, tc.want)
			}
			if m.activeTab != tc.wantActive {
				t.Errorf("activeTab = %d, want %d", m.activeTab, tc.wantActive)
			}
		})
	}
}

// TestModel_ClearDragState asserts the invariant that clearDragState
// zeros every mutually-exclusive drag flag in one place. A regression
// where a new drag mode is added but not cleared here would silently
// allow two drags to coexist.
func TestModel_ClearDragState(t *testing.T) {
	t.Parallel()
	m := Model{
		tabDragFromIdx:   5,
		scrollDragPaneID: "pane-x",
		scrollDragRect:   PaneRect{OX: 1, OY: 2, W: 3, H: 4},
		mouseDown:        true,
		notesMouseDown:   true,
	}
	m.clearDragState()
	if m.tabDragFromIdx != -1 {
		t.Errorf("tabDragFromIdx = %d, want -1", m.tabDragFromIdx)
	}
	if m.scrollDragPaneID != "" {
		t.Errorf("scrollDragPaneID = %q, want empty", m.scrollDragPaneID)
	}
	if m.scrollDragRect != (PaneRect{}) {
		t.Errorf("scrollDragRect = %+v, want zero value", m.scrollDragRect)
	}
	if m.mouseDown {
		t.Error("mouseDown = true, want false")
	}
	if m.notesMouseDown {
		t.Error("notesMouseDown = true, want false")
	}
}
