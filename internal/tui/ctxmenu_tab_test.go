package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/ipc"
)

// newTabCtxMenuTestModel mirrors newSplitDragTestModel's dimensions (100x40,
// row 0 tab bar) and appends a second, background tab — "Build" — so a
// right-click on the tab bar has a NON-active tab to target. Tab 0 ("T",
// active, the p1|p2 split) stays exactly as newSplitDragTestModel built it.
func newTabCtxMenuTestModel(t *testing.T) *Model {
	t.Helper()
	m := newSplitDragTestModel(t)
	m.appendTab(NewTabModel("tab-2", "Build"))
	return m
}

// sidebarTabRowCoords resolves the screen coordinate of the sidebar tab
// heading row for curTabs() index `idx`, scanning the same row list
// sidebarRowAt indexes — mirroring sidebarPaneRowCoords rather than
// hardcoding row geometry that shifts whenever a fixture gains a row.
func sidebarTabRowCoords(t *testing.T, m *Model, idx int) (int, int) {
	t.Helper()
	for y, row := range m.sidebarVisibleRows(m.projectSidebarWidth(), m.sidebarContentHeight()) {
		if row.kind == sidebarRowTab && row.index == idx {
			return 3, y // column 3: same arbitrary in-strip column sidebarPaneRowCoords uses
		}
	}
	t.Fatalf("no sidebar tab row for index %d", idx)
	return 0, 0
}

// renameTabItemIndex scans an open tab menu for the Rename tab row.
func renameTabItemIndex(t *testing.T, items []ctxMenuItem) int {
	t.Helper()
	for i, it := range items {
		if it.id == ctxActRenameTab {
			return i
		}
	}
	t.Fatal("Rename tab item not found in the tab menu")
	return -1
}

// colorListItemIndex scans an open tab menu for the Set color… row.
func colorListItemIndex(t *testing.T, items []ctxMenuItem) int {
	t.Helper()
	for i, it := range items {
		if it.id == ctxActTabColorList {
			return i
		}
	}
	t.Fatal("Set color… item not found in the tab menu")
	return -1
}

func TestTabCtxMenu_RightClickTabBarOpensForTabUnderCursor(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)

	target := got.curTabs()[1]
	if got.ctxMenu.tabID != target.ID {
		t.Errorf("ctxMenu.tabID = %q, want %q", got.ctxMenu.tabID, target.ID)
	}
	if got.ctxMenu.paneID != "" || got.ctxMenu.projectID != "" {
		t.Errorf("paneID/projectID should stay empty for a tab menu: paneID=%q projectID=%q",
			got.ctxMenu.paneID, got.ctxMenu.projectID)
	}
	if got.ctxMenu.title != target.Name {
		t.Errorf("title = %q, want the target tab's name %q", got.ctxMenu.title, target.Name)
	}
	if got.activeTabIdx() != 0 {
		t.Errorf("activeTabIdx changed to %d — right-click must not switch tabs", got.activeTabIdx())
	}
}

// TestTabCtxMenu_RightClickAnotherTabRetargetsInOneGesture pins the existing
// close-then-fall-through at the top of the mouse-right-click handling
// (unchanged by this feature): a right-click outside the open menu closes it
// and, being itself a right-click, falls through to open a new one — so
// right-clicking a second tab re-targets the menu in one gesture, the same
// way TestCtxMenu_RightClickElsewhereRetargets pins it for panes.
func TestTabCtxMenu_RightClickAnotherTabRetargetsInOneGesture(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	x0 := tabBarX(t, m, 0, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x0, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.tabID != got.curTabs()[0].ID {
		t.Fatalf("menu should target tab 0 first: tabID = %q", got.ctxMenu.tabID)
	}

	x1 := tabBarX(t, &got, 1, 0)
	updated, _ = got.Update(tea.MouseClickMsg{X: x1, Y: 0, Button: tea.MouseRight})
	got2 := updated.(Model)
	want := got2.curTabs()[1].ID
	if got2.ctxMenu.tabID != want {
		t.Errorf("right-click on another tab should retarget in one gesture: tabID = %q, want %q",
			got2.ctxMenu.tabID, want)
	}
}

func TestTabCtxMenu_RightClickTabBarOffAnyTabDoesNothing(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	if idx := m.hitTestTab(m.width - 1); idx >= 0 {
		t.Fatalf("fixture: column %d should be past the last tab, hit-tests to %d", m.width-1, idx)
	}
	updated, _ := m.Update(tea.MouseClickMsg{X: m.width - 1, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("right-click past the last tab must not open any menu")
	}
}

func TestTabCtxMenu_RightClickTabBarWithSelectionCopiesInstead(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	m.selection = &Selection{PaneID: "p1"}
	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu must NOT open while a selection is active (copy wins)")
	}
	if got.selection != nil {
		t.Error("right-click should consume the selection (copy path)")
	}
}

func TestTabCtxMenu_RightClickSidebarTabRowOpens(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	x, y := sidebarTabRowCoords(t, &m, 1)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)

	want := got.curTabs()[1].ID
	if got.ctxMenu.tabID != want {
		t.Errorf("ctxMenu.tabID = %q, want %q", got.ctxMenu.tabID, want)
	}
	if got.activeTabIdx() != 0 {
		t.Errorf("activeTabIdx changed to %d — right-click must not switch tabs", got.activeTabIdx())
	}
	if got.sidebarTabDragging {
		t.Error("right-click must not arm the sidebar tab reorder drag")
	}
}

// TestTabCtxMenu_SurvivesUnrelatedMessages pins the prologue arm added for
// the tab kind, following TestCtxMenu_ProjectMenuSurvivesUnrelatedMessages:
// a tab menu's paneID is empty, so testing paneID alone would close it on
// the very next message.
func TestTabCtxMenu_SurvivesUnrelatedMessages(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should have opened")
	}
	wantTabID := got.ctxMenu.tabID

	updated, _ = got.Update(workSpinnerTickMsg{})
	got = updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.tabID != wantTabID {
		t.Fatal("the tab menu closed itself on an unrelated spinner tick")
	}

	// Poll-echo shape (model_sizepoll_test.go): sized + matching pending
	// dimensions makes an unchanged WindowSizeMsg a pure no-op rather than
	// tripping the first-resize/attach machinery this fixture has no client
	// wired up for.
	got.sized = true
	got.pendingWidth = got.width
	got.pendingHeight = got.height
	updated, _ = got.Update(tea.WindowSizeMsg{Width: got.width, Height: got.height})
	got = updated.(Model)
	if !got.ctxMenu.open() || got.ctxMenu.tabID != wantTabID {
		t.Fatal("the tab menu closed itself on an unchanged window-size message")
	}
}

func TestTabCtxMenu_VanishedTabClosesOnNextMessage(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should have opened")
	}

	proj := got.cur()
	proj.tabs = append(proj.tabs[:1], proj.tabs[2:]...) // drop the targeted tab

	updated, _ = got.Update(workSpinnerTickMsg{})
	if updated.(Model).ctxMenu.open() {
		t.Error("menu must close when its tab no longer exists")
	}
}

func TestTabCtxMenu_RenameSwitchesToTabAndEntersRenameMode(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	fake := newFakeConn()
	m.client = fake

	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	target := got.curTabs()[1]

	renameIdx := renameTabItemIndex(t, got.ctxMenu.items)
	clickY := got.ctxMenu.itemScreenY(renameIdx)
	clickX := got.ctxMenu.x + 1

	updated, cmd := got.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	got2 := updated.(Model)

	if got2.activeTabIdx() != 1 {
		t.Errorf("activeTabIdx = %d, want 1 (Rename switches to the target tab)", got2.activeTabIdx())
	}
	if !got2.renaming {
		t.Error("renaming should be true after choosing Rename tab")
	}
	if got2.renameInput != target.Name {
		t.Errorf("renameInput = %q, want the target tab's name %q", got2.renameInput, target.Name)
	}
	if got2.tabScrollAnchor != "" {
		t.Errorf("tabScrollAnchor = %q, want empty (beginTabRename clears it)", got2.tabScrollAnchor)
	}
	if cmd == nil {
		t.Fatal("no cmd returned — Rename on a non-active tab must send MsgSwitchTab")
	}
	cmd()

	var switched *ipc.Message
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchTab {
			switched = msg
		}
	}
	if switched == nil {
		t.Fatal("no MsgSwitchTab sent")
	}
	var payload ipc.SwitchTabPayload
	if err := switched.DecodePayload(&payload); err != nil {
		t.Fatalf("decode switch payload: %v", err)
	}
	if payload.TabID != target.ID {
		t.Errorf("MsgSwitchTab TabID = %q, want %q", payload.TabID, target.ID)
	}
}

func TestTabCtxMenu_RenameOnActiveTabSendsNoSwitch(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	fake := newFakeConn()
	m.client = fake

	x := tabBarX(t, m, 0, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)

	renameIdx := renameTabItemIndex(t, got.ctxMenu.items)
	clickY := got.ctxMenu.itemScreenY(renameIdx)
	clickX := got.ctxMenu.x + 1

	updated, cmd := got.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	got2 := updated.(Model)

	if got2.activeTabIdx() != 0 {
		t.Errorf("activeTabIdx changed to %d, want 0 (rename on the already-active tab)", got2.activeTabIdx())
	}
	if !got2.renaming {
		t.Error("renaming should be true after choosing Rename tab")
	}
	if cmd != nil {
		cmd()
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchTab {
			t.Error("Rename on the already-active tab must not send MsgSwitchTab")
		}
	}
}

// TestTabCtxMenu_RefusesWhenActiveProjectMoved pins the same uniform refusal
// the pane menu makes (TestCtxMenu_ExecuteRefusesWhenTheActiveTabMovedAway):
// MCP switch_project is the one producer that can move the active project
// underneath an open menu with no keyboard or mouse event involved.
func TestTabCtxMenu_RefusesWhenActiveProjectMoved(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	m.projects = append(m.projects, &ProjectModel{
		ID: "proj-b", Name: "beta",
		tabs: []*TabModel{tabWith(&PaneModel{ID: "pane-b"})},
	})

	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if !got.ctxMenu.open() {
		t.Fatal("menu should have opened")
	}
	origActiveTab := got.projects[0].activeTab

	got.activeProject = 1 // moved underneath the open menu, the MCP shape

	renameIdx := renameTabItemIndex(t, got.ctxMenu.items)
	updated, _ = got.executeCtxMenuItem(got.ctxMenu.items[renameIdx])
	got2 := updated.(Model)

	if got2.renaming {
		t.Error("Rename must be refused once the active project moved underneath the menu")
	}
	if got2.projects[0].activeTab != origActiveTab {
		t.Error("the tab's own project's active tab must not change on a refused execute")
	}
}

func TestTabCtxMenu_SetColorReopensWithPalette(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	target := got.curTabs()[1]
	target.Color = "4" // Blue — lands the current-colour marker mid-list

	colorListIdx := colorListItemIndex(t, got.ctxMenu.items)
	updated, _ = got.executeCtxMenuItem(got.ctxMenu.items[colorListIdx])
	got2 := updated.(Model)

	if !got2.ctxMenu.open() || got2.ctxMenu.tabID != target.ID {
		t.Fatal("the menu should stay open, re-targeted at the same tab")
	}
	if len(got2.ctxMenu.items) != len(tabColors) {
		t.Fatalf("item count = %d, want %d (one row per tabColors entry)", len(got2.ctxMenu.items), len(tabColors))
	}
	for i, it := range got2.ctxMenu.items {
		if it.color != tabColors[i] {
			t.Errorf("items[%d].color = %q, want %q (palette order)", i, it.color, tabColors[i])
		}
	}
	if c := got2.ctxMenu.cursor; c < 0 || c >= len(got2.ctxMenu.items) || got2.ctxMenu.items[c].color != target.Color {
		t.Errorf("cursor = %d, want it on the tab's current colour %q", c, target.Color)
	}
	checks := 0
	for _, it := range got2.ctxMenu.items {
		if strings.HasPrefix(it.label, "✓ ") {
			checks++
		}
	}
	if checks != 1 {
		t.Errorf("exactly one label should start with \"✓ \", got %d", checks)
	}
	w, h := got2.ctxMenu.boxSize()
	if got2.ctxMenu.x < 0 || got2.ctxMenu.x+w > got2.width ||
		got2.ctxMenu.y < 1 || got2.ctxMenu.y+h > got2.height-1 {
		t.Errorf("box (%d,%d,%dx%d) escapes the content area (%dx%d)",
			got2.ctxMenu.x, got2.ctxMenu.y, w, h, got2.width, got2.height)
	}
}

func TestTabCtxMenu_ChoosingColorSendsUpdateTabForTheTargetTab(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	fake := newFakeConn()
	m.client = fake

	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	target := got.curTabs()[1]

	colorListIdx := colorListItemIndex(t, got.ctxMenu.items)
	updated, _ = got.executeCtxMenuItem(got.ctxMenu.items[colorListIdx])
	got2 := updated.(Model)

	blueIdx := -1
	for i, c := range tabColors {
		if c == "4" {
			blueIdx = i
		}
	}
	if blueIdx < 0 {
		t.Fatal("fixture assumption: tabColors must contain Blue (\"4\")")
	}
	got2.ctxMenu.cursor = blueIdx

	updated2, cmd := got2.handleCtxMenuKey("enter")
	got3 := updated2.(Model)

	if got3.ctxMenu.open() {
		t.Error("menu should close after choosing a colour")
	}
	if target.Color != "4" {
		t.Errorf("tab.Color = %q, want \"4\" (optimistic local write)", target.Color)
	}
	if got3.activeTabIdx() != 0 {
		t.Errorf("activeTabIdx changed to %d — choosing a colour must not switch tabs", got3.activeTabIdx())
	}
	if cmd == nil {
		t.Fatal("no cmd returned — choosing a colour must send MsgUpdateTab")
	}
	cmd()

	var updateMsg *ipc.Message
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgUpdateTab {
			updateMsg = msg
		}
	}
	if updateMsg == nil {
		t.Fatal("no MsgUpdateTab sent")
	}
	var payload ipc.UpdateTabPayload
	if err := updateMsg.DecodePayload(&payload); err != nil {
		t.Fatalf("decode update payload: %v", err)
	}
	if payload.TabID != target.ID || payload.Name != target.Name || payload.Color != "4" || payload.ClearColor {
		t.Errorf("MsgUpdateTab = %+v, want TabID=%q Name=%q Color=\"4\" ClearColor=false",
			payload, target.ID, target.Name)
	}
}

func TestTabCtxMenu_ChoosingDefaultSendsClearColor(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	fake := newFakeConn()
	m.client = fake

	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	target := got.curTabs()[1]
	target.Color = "2" // start non-default so the clear is observable

	colorListIdx := colorListItemIndex(t, got.ctxMenu.items)
	updated, _ = got.executeCtxMenuItem(got.ctxMenu.items[colorListIdx])
	got2 := updated.(Model)
	got2.ctxMenu.cursor = 0 // tabColors[0] == "" (Default) is always first

	updated2, cmd := got2.handleCtxMenuKey("enter")
	got3 := updated2.(Model)

	if got3.ctxMenu.open() {
		t.Error("menu should close after choosing a colour")
	}
	if target.Color != "" {
		t.Errorf("tab.Color = %q, want \"\" (Default)", target.Color)
	}
	if cmd == nil {
		t.Fatal("no cmd returned")
	}
	cmd()

	var updateMsg *ipc.Message
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgUpdateTab {
			updateMsg = msg
		}
	}
	if updateMsg == nil {
		t.Fatal("no MsgUpdateTab sent")
	}
	var payload ipc.UpdateTabPayload
	if err := updateMsg.DecodePayload(&payload); err != nil {
		t.Fatalf("decode update payload: %v", err)
	}
	if payload.Color != "" || !payload.ClearColor {
		t.Errorf("MsgUpdateTab Color=%q ClearColor=%v, want \"\" / true", payload.Color, payload.ClearColor)
	}
}

func TestTabCtxMenu_NotOpenedInNotesMode(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	m.notesMode = true
	x := tabBarX(t, m, 1, 0)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("the tab menu must not open while notes mode owns input")
	}
}

// TestTabCtxMenu_SidebarRightClickNotOpenedInNotesMode pins the same refusal
// as the test above, but through the SIDEBAR entry point rather than the tab
// bar — and the two are not redundant. The tab-bar right-click has its OWN
// call-site gate before it ever calls openTabCtxMenu (`m.dialog == dialogNone
// && !m.notesMode && !m.renaming && !m.renamingPane`, model.go's mouse-right
// branch), so that path would still refuse even without openTabCtxMenu's own
// check. The sidebar's `projectSidebarSwallowsMouse` branch has no such
// call-site gate: its `sidebarRowTab` case dispatches straight into
// openTabCtxMenu, so THIS test is the only thing in the suite that would
// notice if openTabCtxMenu's own refusal (notes mode / an inline rename / a
// pane rename / an open dialog) were ever removed.
func TestTabCtxMenu_SidebarRightClickNotOpenedInNotesMode(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	m.notesMode = true
	x, y := sidebarTabRowCoords(t, &m, 1)
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("the sidebar tab menu must not open while notes mode owns input")
	}
}

func TestTabCtxMenu_NarrowTerminalGuard(t *testing.T) {
	t.Parallel()
	m := newTabCtxMenuTestModel(t)
	m.width = 10 // narrower than the ~14-col tab menu box
	if idx := m.hitTestTab(0); idx < 0 {
		t.Fatal("fixture: column 0 should still hit-test to the active tab at this width")
	}
	updated, _ := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseRight})
	got := updated.(Model)
	if got.ctxMenu.open() {
		t.Error("menu must not open when its box cannot fit inside the content area")
	}
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestTabColorLabel_CoversThePalette(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, c := range tabColors {
		label := tabColorLabel(c)
		if label == "" {
			t.Errorf("tabColorLabel(%q) = \"\", every tabColors entry must have a name", c)
		}
		if seen[label] {
			t.Errorf("label %q used for more than one tabColors entry", label)
		}
		seen[label] = true
	}
}

// TestRenderCtxMenu_ColorListDimensions follows TestRenderCtxMenu_Dimensions:
// the rendered box's line count and per-line width must equal boxSize(),
// regardless of the coloured foreground styling the colour rows add.
func TestRenderCtxMenu_ColorListDimensions(t *testing.T) {
	t.Parallel()
	items := buildTabColorItems("4")
	for _, spaced := range []bool{true, false} {
		s := ctxMenuState{
			tabID:  "t",
			title:  "Build",
			cursor: 1,
			spaced: spaced,
			items:  items,
		}
		out := renderCtxMenu(s)
		lines := strings.Split(out, "\n")
		w, h := s.boxSize()
		if len(lines) != h {
			t.Fatalf("spaced=%v: rendered height = %d, want %d", spaced, len(lines), h)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != w {
				t.Errorf("spaced=%v: line %d width = %d, want %d", spaced, i, got, w)
			}
		}
	}
}
