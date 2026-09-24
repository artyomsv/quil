package tui

import (
	"math"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/keymap"
)

// newLayoutTestModel builds one actionable project ("proj-arrange", Dest "")
// holding tab-a (ACTIVE: p1|p2, active p1) and tab-b (background:
// p3|(p4/p5), active p4) at w x h, no project sidebar. Real PaneModels,
// because Resize drives the VT emulator.
func newLayoutTestModel(t *testing.T, w, h int) (*Model, *fakeConn) {
	t.Helper()
	m := newModelForTest(nil, 0)
	m.width, m.height = w, h
	m.notifications = NewNotificationCenter(30, 200)
	m.mcpHighlights = make(map[string]bool)
	fake := newFakeConn()
	m.client = fake

	p1, p2 := NewPaneModel("p1", 1024), NewPaneModel("p2", 1024)
	ta := NewTabModel("tab-a", "Shell")
	ta.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p1), Right: NewLeaf(p2)}
	ta.ActivePane, p1.Active = "p1", true

	p3, p4, p5 := NewPaneModel("p3", 1024), NewPaneModel("p4", 1024), NewPaneModel("p5", 1024)
	tb := NewTabModel("tab-b", "Build")
	tb.Root = &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p3),
		Right: &LayoutNode{Split: SplitVertical, Ratio: 0.5, Left: NewLeaf(p4), Right: NewLeaf(p5)}}
	tb.ActivePane, p4.Active = "p4", true

	m.projects = []*ProjectModel{{ID: "proj-arrange", Name: "arrange", tabs: []*TabModel{ta, tb}}}
	m.activeProject = 0
	m.resizeTabs()
	return &m, fake
}

// layMenuRow returns the index of the row with id in an open menu.
func layMenuRow(t *testing.T, s ctxMenuState, id ctxMenuAction) int {
	t.Helper()
	for i, it := range s.items {
		if it.id == id {
			return i
		}
	}
	t.Fatalf("menu has no row with id %d (open=%v)", id, s.open())
	return -1
}

// layOpenList right-clicks tab tabIdx in the tab bar and clicks Layout…,
// both through Update.
func layOpenList(t *testing.T, m *Model, tabIdx int) Model {
	t.Helper()
	updated, _ := m.Update(tea.MouseClickMsg{X: tabBarX(t, m, tabIdx, 1), Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	row := layMenuRow(t, got.ctxMenu, ctxActTabLayoutList)
	if !got.ctxMenu.items[row].enabled {
		t.Fatal("setup: the Layout… row is greyed")
	}
	updated, _ = got.Update(tea.MouseClickMsg{X: got.ctxMenu.x + 1, Y: got.ctxMenu.itemScreenY(row), Button: tea.MouseLeft})
	got = updated.(Model)
	if !got.ctxMenu.open() || len(got.ctxMenu.items) != len(layoutPresets) {
		t.Fatalf("setup: Layout… did not re-populate the menu (open=%v, items=%d)", got.ctxMenu.open(), len(got.ctxMenu.items))
	}
	return got
}

// layChoose left-clicks the list row for kind through Update.
func layChoose(t *testing.T, m Model, kind layoutKind) (Model, tea.Cmd) {
	t.Helper()
	for i, it := range m.ctxMenu.items {
		if it.id == ctxActTabLayout && it.layout == kind {
			updated, cmd := m.Update(tea.MouseClickMsg{X: m.ctxMenu.x + 1, Y: m.ctxMenu.itemScreenY(i), Button: tea.MouseLeft})
			return updated.(Model), cmd
		}
	}
	t.Fatalf("no list row for layout kind %d", kind)
	return m, nil
}

// laySentLayouts decodes every MsgUpdateLayout the fake connection received.
func laySentLayouts(t *testing.T, fake *fakeConn) []ipc.UpdateLayoutPayload {
	t.Helper()
	var out []ipc.UpdateLayoutPayload
	for _, msg := range fake.sent {
		if msg.Type != ipc.MsgUpdateLayout {
			continue
		}
		var p ipc.UpdateLayoutPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode update_layout: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// layBind binds action id to f9 and returns an f9 press.
func layBind(t *testing.T, m *Model, id keymap.ActionID) tea.KeyPressMsg {
	t.Helper()
	m.SetBindings(config.Bindings{Overrides: map[keymap.ActionID]string{id: "f9"}})
	msg, ok := keyPressForChord("f9")
	if !ok {
		t.Fatal("no key encoding for f9")
	}
	return msg
}

func TestLayoutPresets_EveryActionIsRegisteredAndUnbound(t *testing.T) {
	t.Parallel()
	if len(layoutPresets) != 6 {
		t.Fatalf("layoutPresets has %d rows, want 6", len(layoutPresets))
	}
	for _, p := range layoutPresets {
		a, ok := keymap.Lookup(p.action)
		if !ok {
			t.Errorf("preset %q names an unregistered action", p.action)
			continue
		}
		if a.Default != "" {
			t.Errorf("action %q ships bound to %q, want unbound", p.action, a.Default)
		}
		if k, ok := layoutKindFor(p.action); !ok || k != p.kind {
			t.Errorf("layoutKindFor(%q) = (%d,%v), want (%d,true)", p.action, k, ok, p.kind)
		}
	}
	if _, ok := layoutKindFor("tab.close"); ok {
		t.Error("layoutKindFor must refuse an action that is not a layout")
	}
	for _, it := range buildTabLayoutItems(false) {
		if it.enabled {
			t.Errorf("row %q is enabled in a list built for a tab that cannot be arranged", it.label)
		}
	}
}

// Review Focus 4.
func TestTabLayoutMenu_ColumnsArrangesTheTabItWasOpenedOn(t *testing.T) {
	t.Parallel()
	m, fake := newLayoutTestModel(t, 100, 40)
	got := layOpenList(t, m, 1)
	got, cmd := layChoose(t, got, layoutColumns)
	runCmd(cmd)

	tb := got.tabByID("tab-b")
	if s := arrTree(tb.Root); s != "(p3|(p4|p5))" {
		t.Fatalf("tab-b tree = %s, want (p3|(p4|p5))", s)
	}
	if s := arrTree(got.tabByID("tab-a").Root); s != "(p1|p2)" {
		t.Errorf("tab-a tree = %s — the arrangement leaked onto the active tab", s)
	}
	if got.activeTabIdx() != 0 {
		t.Errorf("activeTabIdx = %d, want 0 — choosing a layout must not switch tabs", got.activeTabIdx())
	}
	if got.ctxMenu.open() {
		t.Error("the menu must close after a choice")
	}
	// Resized with the canonical geometry, 100x38: thirds of 100 are 33/33/34.
	if w3, w5 := mpPane(t, tb, "p3").Width, mpPane(t, tb, "p5").Width; w3 != 33 || w5 != 34 {
		t.Errorf("widths p3=%d p5=%d, want 33 and 34 — tab-b was not resized", w3, w5)
	}
	sends := laySentLayouts(t, fake)
	if len(sends) != 1 {
		t.Fatalf("MsgUpdateLayout sent %d times, want exactly 1", len(sends))
	}
	if sends[0].TabID != "tab-b" {
		t.Errorf("layout sent for %q, want tab-b", sends[0].TabID)
	}
	if !layoutAgrees(sends[0].Layout, tb.Root) {
		t.Error("the sent layout does not match tab-b's new tree")
	}
}

func TestTabLayoutMenu_EachChoiceBuildsItsPreset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind layoutKind
		want string
	}{
		{layoutEven, "(p3|(p4/p5))"},
		{layoutColumns, "(p3|(p4|p5))"},
		{layoutRows, "(p3/(p4/p5))"},
		{layoutGrid, "((p3|p4)/p5)"},
		{layoutMain, "(p4|(p3/p5))"}, // the tab's ACTIVE pane is main
		{layoutSpiral, "(p3|(p4/p5))"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			m, _ := newLayoutTestModel(t, 100, 40)
			got := layOpenList(t, m, 1)
			got, _ = layChoose(t, got, tc.kind)
			root := got.tabByID("tab-b").Root
			if s := arrTree(root); s != tc.want {
				t.Fatalf("kind %d: tree = %s, want %s", tc.kind, s, tc.want)
			}
			if tc.kind == layoutEven || tc.kind == layoutSpiral {
				if math.Abs(root.Ratio-1.0/3) > 1e-9 || math.Abs(root.Right.Ratio-0.5) > 1e-9 {
					t.Errorf("ratios %v/%v, want 1/3 and 1/2", root.Ratio, root.Right.Ratio)
				}
			}
		})
	}
}

func TestTabLayoutMenu_KeepsTheTabsActivePane(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, 40)
	got := layOpenList(t, m, 1)
	got, _ = layChoose(t, got, layoutRows)
	tb := got.tabByID("tab-b")
	if tb.ActivePane != "p4" {
		t.Errorf("ActivePane = %q, want p4", tb.ActivePane)
	}
	for _, id := range []string{"p3", "p4", "p5"} {
		if want := id == "p4"; mpPane(t, tb, id).Active != want {
			t.Errorf("%s.Active = %v, want %v", id, !want, want)
		}
	}
}

func TestTabLayoutMenu_ExitsTheTabsFocusMode(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, 40)
	m.tabByID("tab-b").ToggleFocus()
	got := layOpenList(t, m, 1)
	got, _ = layChoose(t, got, layoutRows)
	if got.tabByID("tab-b").FocusMode() {
		t.Error("arranging a tab must exit that tab's focus mode")
	}
}

// Review Focus 3 (greyed half).
func TestTabCtxMenu_LayoutRowGreyedWhenTheTabCannotBeArranged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		arm  func(m *Model, tb *TabModel)
	}{
		{"a single pane", func(m *Model, tb *TabModel) {
			tb.Root = NewLeaf(tb.Root.Left.Pane)
			tb.invalidateLeaves()
		}},
		{"a worktree create in flight", func(m *Model, tb *TabModel) {
			m.worktreeCreates = map[string]string{"tab-b": "feat-x"}
		}},
		{"a worktree replace in flight", func(m *Model, tb *TabModel) {
			m.worktreeReplaced = map[string]*PaneModel{"tab-b": NewPaneModel("held", 1024)}
		}},
		{"a template not laid out yet", func(m *Model, tb *TabModel) { tb.templateLayoutPending = true }},
		{"a leaf preparing its worktree", func(m *Model, tb *TabModel) {
			tb.Root.Right.Right.Pane.PreparingWorktree = "feat-x"
		}},
		{"this client's own pending split", func(m *Model, tb *TabModel) {
			m.pendingSplit = map[string]*LayoutNode{"tab-b": tb.SplitAtPane("p5", SplitVertical)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newLayoutTestModel(t, 100, 40)
			tc.arm(m, m.tabByID("tab-b"))
			updated, _ := m.Update(tea.MouseClickMsg{X: tabBarX(t, m, 1, 1), Y: 0, Button: tea.MouseRight})
			got := updated.(Model)
			row := layMenuRow(t, got.ctxMenu, ctxActTabLayoutList)
			if got.ctxMenu.items[row].enabled {
				t.Fatal("Layout… is enabled for a tab that cannot be arranged")
			}
			updated, _ = got.Update(tea.MouseClickMsg{X: got.ctxMenu.x + 1, Y: got.ctxMenu.itemScreenY(row), Button: tea.MouseLeft})
			if items := updated.(Model).ctxMenu.items; len(items) > 0 && items[0].id == ctxActTabLayout {
				t.Error("clicking the greyed Layout… row opened the list")
			}
		})
	}
}

// Review Focus 3 (race half): the list was built while the tab was
// arrangeable; this client arms a split in it before the choice.
func TestTabLayoutMenu_ReservationArmedAfterOpeningRefusesWithAFlash(t *testing.T) {
	t.Parallel()
	m, fake := newLayoutTestModel(t, 100, 40)
	got := layOpenList(t, m, 1)
	tb := got.tabByID("tab-b")
	ph := tb.SplitAtPane("p5", SplitVertical)
	got.pendingSplit = map[string]*LayoutNode{"tab-b": ph}
	before, shape := tb.Root, arrTree(tb.Root)

	// The returned cmd is flashCmd's 3 s tick: never run it.
	got, _ = layChoose(t, got, layoutColumns)

	if tb.Root != before || arrTree(tb.Root) != shape {
		t.Errorf("tree = %s, want it untouched (%s)", arrTree(tb.Root), shape)
	}
	if !treeContains(tb.Root, ph) {
		t.Error("the reservation was detached — pendingSplit now points at a node no tree holds")
	}
	if got.flashText != tabBusyFlash {
		t.Errorf("flash = %q, want %q", got.flashText, tabBusyFlash)
	}
	if n := len(laySentLayouts(t, fake)); n != 0 {
		t.Errorf("sent %d layouts for a refused arrangement", n)
	}
}

func TestTabLayoutKey_BusyTabFlashes(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, 40)
	press := layBind(t, m, "tab.layout_rows")
	m.worktreeCreates = map[string]string{"tab-a": "feat-x"}
	before := m.tabByID("tab-a").Root

	updated, _ := m.Update(press)
	got := updated.(Model)

	if got.tabByID("tab-a").Root != before {
		t.Error("a busy tab was arranged")
	}
	if got.flashText != tabBusyFlash {
		t.Errorf("flash = %q, want %q", got.flashText, tabBusyFlash)
	}
}

func TestTabLayoutKey_TooSmallForTheTerminalRefusesWithAFlash(t *testing.T) {
	t.Parallel()
	m, fake := newLayoutTestModel(t, 100, minTermHeight) // pane area 100x8
	m.projects[0].activeTab = 1                          // tab-b: three panes; three rows need 12
	press := layBind(t, m, "tab.layout_rows")
	tb := m.tabByID("tab-b")
	before, shape := tb.Root, arrTree(tb.Root)

	updated, _ := m.Update(press)
	got := updated.(Model)

	if tb.Root != before || arrTree(tb.Root) != shape {
		t.Errorf("tree = %s, want it untouched (%s)", arrTree(tb.Root), shape)
	}
	if got.flashText != layoutTooSmallFlash {
		t.Errorf("flash = %q, want %q", got.flashText, layoutTooSmallFlash)
	}
	if n := len(laySentLayouts(t, fake)); n != 0 {
		t.Errorf("sent %d layouts for a refused arrangement", n)
	}
}

// Review Focus 2: equal areas are not a minimum height.
func TestTabLayoutKey_EvenOutThatWouldBeTooSmallIsRefused(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, minTermHeight) // pane area 100x8
	tb := m.tabByID("tab-b")
	p3, p4, p5 := mpPane(t, tb, "p3"), mpPane(t, tb, "p4"), mpPane(t, tb, "p5")
	p6, p7 := NewPaneModel("p6", 1024), NewPaneModel("p7", 1024)
	// Two panes over three, halves everywhere: 4 rows each, which fits.
	tb.Root = &LayoutNode{Split: SplitVertical, Ratio: 0.5,
		Left: &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p3), Right: NewLeaf(p4)},
		Right: &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p5),
			Right: &LayoutNode{Split: SplitHorizontal, Ratio: 0.5, Left: NewLeaf(p6), Right: NewLeaf(p7)}}}
	tb.invalidateLeaves()
	if !fitsMinSize(tb.Root, 100, 8) {
		t.Fatal("fixture: the lopsided tree must fit 100x8")
	}
	if fitsMinSize(evenOut(tb.Root), 100, 8) {
		t.Fatal("fixture: the evened tree (top row 2/5 of 8 = 3 rows) must NOT fit 100x8")
	}
	m.projects[0].activeTab = 1
	press := layBind(t, m, "tab.layout_even")
	before := tb.Root

	updated, _ := m.Update(press)
	got := updated.(Model)

	if tb.Root != before || tb.Root.Ratio != 0.5 {
		t.Errorf("tree ratio = %v, want the refused even-out to leave 0.5", tb.Root.Ratio)
	}
	if got.flashText != layoutTooSmallFlash {
		t.Errorf("flash = %q, want %q", got.flashText, layoutTooSmallFlash)
	}
}

func TestTabLayoutKey_RefusedInNotesMode(t *testing.T) {
	t.Parallel()
	m, fake := newLayoutTestModel(t, 100, 40)
	press := layBind(t, m, "tab.layout_rows")
	m.notesMode = true
	before := m.tabByID("tab-a").Root

	updated, cmd := m.Update(press)
	runCmd(cmd)
	got := updated.(Model)

	if got.tabByID("tab-a").Root != before {
		t.Error("a layout key rearranged the tab under the notes editor")
	}
	if n := len(laySentLayouts(t, fake)); n != 0 {
		t.Errorf("sent %d layouts in notes mode", n)
	}
}

func TestTabLayoutPalette_ArrangesTheActiveTab(t *testing.T) {
	t.Parallel()
	m, fake := newLayoutTestModel(t, 100, 40)
	opened, _ := m.openCommandPalette()
	got := opened.(Model)
	got.palette.cursor = -1
	for i, c := range got.paletteDisplay() {
		if c.action == palActTabLayout && c.arg == "tab.layout_rows" {
			got.palette.cursor = i
		}
	}
	if got.palette.cursor < 0 {
		t.Fatal("the palette has no row for tab.layout_rows")
	}

	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runCmd(cmd)
	got = updated.(Model)

	if s := arrTree(got.tabByID("tab-a").Root); s != "(p1/p2)" {
		t.Errorf("active tab tree = %s, want (p1/p2)", s)
	}
	if s := arrTree(got.tabByID("tab-b").Root); s != "(p3|(p4/p5))" {
		t.Errorf("background tab tree = %s, want it untouched", s)
	}
	if sends := laySentLayouts(t, fake); len(sends) != 1 || sends[0].TabID != "tab-a" {
		t.Errorf("layout sends = %+v, want exactly one, for tab-a", sends)
	}
}

func TestTabLayoutPalette_RowsGreyedForASinglePaneTabAndShowABoundKey(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, 40)
	layBind(t, m, "tab.layout_grid")
	rows := paletteRows(m, palActTabLayout)
	if len(rows) != len(layoutPresets) {
		t.Fatalf("palette has %d layout rows, want %d", len(rows), len(layoutPresets))
	}
	for _, r := range rows {
		if !r.enabled {
			t.Errorf("row %q greyed for an arrangeable tab", r.label)
		}
		if r.arg == "tab.layout_grid" && r.detail != "f9" {
			t.Errorf("grid row detail = %q, want the bound key f9", r.detail)
		}
	}
	ta := m.tabByID("tab-a")
	ta.Root = NewLeaf(ta.Root.Left.Pane)
	ta.invalidateLeaves()
	for _, r := range paletteRows(m, palActTabLayout) {
		if r.enabled {
			t.Errorf("row %q enabled for a single-pane tab", r.label)
		}
	}
}

func TestTabCtxMenu_LayoutRowSitsAfterSetColorAndBeforeMove(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, 40)
	m.projects = append(m.projects, &ProjectModel{ID: "proj-other", Name: "other",
		tabs: []*TabModel{NewTabModel("tab-o", "Other")}})
	updated, _ := m.Update(tea.MouseClickMsg{X: tabBarX(t, m, 1, 1), Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	want := []ctxMenuAction{ctxActRenameTab, ctxActTabColorList, ctxActTabLayoutList, ctxActMoveTab}
	if len(got.ctxMenu.items) != len(want) {
		t.Fatalf("tab menu has %d rows, want %d", len(got.ctxMenu.items), len(want))
	}
	for i, id := range want {
		if got.ctxMenu.items[i].id != id {
			t.Errorf("row %d id = %d, want %d", i, got.ctxMenu.items[i].id, id)
		}
	}
	if l := got.ctxMenu.items[2].label; l != "Layout…" {
		t.Errorf("row 2 label = %q, want Layout…", l)
	}
}

// The compact-fallback height check the new row needs: at minTermHeight the
// four-row tab menu still fits; the six-row list does not, and closes the
// menu instead of leaving an invisible one owning input.
func TestTabCtxMenu_FourRowsFitTheMinimumTerminalButTheListClosesTheMenu(t *testing.T) {
	t.Parallel()
	m, _ := newLayoutTestModel(t, 100, minTermHeight)
	m.projects = append(m.projects, &ProjectModel{ID: "proj-other", Name: "other",
		tabs: []*TabModel{NewTabModel("tab-o", "Other")}})
	updated, _ := m.Update(tea.MouseClickMsg{X: tabBarX(t, m, 1, 1), Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() || len(got.ctxMenu.items) != 4 {
		t.Fatalf("the four-row tab menu must open at height %d (open=%v, rows=%d)", minTermHeight, got.ctxMenu.open(), len(got.ctxMenu.items))
	}
	row := layMenuRow(t, got.ctxMenu, ctxActTabLayoutList)
	updated, _ = got.Update(tea.MouseClickMsg{X: got.ctxMenu.x + 1, Y: got.ctxMenu.itemScreenY(row), Button: tea.MouseLeft})
	if updated.(Model).ctxMenu.open() {
		t.Error("a list taller than the content area must close the menu")
	}
}
