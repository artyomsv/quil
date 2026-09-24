package tui

import (
	"errors"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// groupBlockSpanIn is the screen-row extent of group g's block — its header
// and every visible member row. Pure over an already-built slice, like
// tabGroupSpanIn. Gated on inGroup: every row outside a group leaves group at
// its zero value, which is group 0's index.
func groupBlockSpanIn(rows []sidebarRow, g int) (start, size int) {
	start, end := -1, -1
	for y, row := range rows {
		if !row.inGroup || row.group != g {
			continue
		}
		if start < 0 {
			start = y
		}
		end = y
	}
	if start < 0 {
		return 0, 0
	}
	return start, end - start + 1
}

// trackGroupDrag advances a header drag: any row of a group's block counts as
// hovering that group, and the dragged group moves past it at the block's
// midpoint (dragSlot), so a one-row collapsed group crossing a tall expanded
// one does not flip-flop. Saved on release (finishGroupDrag), not per event.
func (m *Model) trackGroupDrag(x, y int) {
	rows, row, ok := m.sidebarDragRows(x, y)
	if !ok || !row.inGroup {
		return
	}
	from := m.groupDragIdx
	if from < 0 || from >= len(m.groups.Groups) {
		return
	}
	start, size := groupBlockSpanIn(rows, row.group)
	if size == 0 {
		return
	}
	to := dragSlot(from, row.group, y, start, size)
	if to == from || !m.groups.moveGroup(from, to) {
		return
	}
	m.groupDragIdx = to
	m.groupDragMoved = true
}

// finishProjectDrag ends a project drag. The reorder already happened on
// motion; the RELEASE decides membership: on a group header the project joins
// that group, and a grouped project released on the PROJECTS heading or on an
// ungrouped project's row leaves its group. Anything else changes nothing —
// including a CLICK: a press that never moved decides nothing, because the
// press switched the active project and may have shifted every row below a
// collapsed group, so its release y no longer names the row it was on.
func (m *Model) finishProjectDrag(x, y int) tea.Cmd {
	idx, moved := m.projectDragIdx, m.projectDragMoved
	m.clearDragState()
	if !moved || idx < 0 || idx >= len(m.projects) {
		return nil
	}
	p := m.projects[idx]
	_, row, ok := m.sidebarDragRows(x, y)
	if !ok {
		return nil
	}
	cur := m.groups.groupOf(p.Dest, p.ID)
	switch {
	case row.kind == sidebarRowGroup:
		if !m.groups.assign(row.index, p.Dest, p.ID) {
			return nil
		}
	case cur >= 0 && (row.ungroupDrop || (row.kind == sidebarRowProject && !row.inGroup)):
		m.groups.unassign(p.Dest, p.ID)
	default:
		return nil
	}
	return m.saveGroupsCmd()
}

// buildProjectGroupItems is the Move to group… list: one row per group (the
// current one marked ✓), then New group…, then No group (✓ when ungrouped).
// Every label carries a two-cell marker so innerWidth — measured on the label
// before styling — stays honest, as buildTabColorItems does. Names are
// sanitized for the label only; the raw name rides on groupName.
//
// The name is also CUT to fit ctxMenuTitleCap. innerWidth caps the box there
// on the premise that no item label is wider, and renderCtxMenu pads each
// label with strings.Repeat(innerW - width) — a group name may be 32 runes, up
// to 64 cells, and an over-wide label makes that count negative and panics.
func buildProjectGroupItems(groups []projectGroup, current int) []ctxMenuItem {
	items := make([]ctxMenuItem, 0, len(groups)+2)
	for i, grp := range groups {
		marker := "  "
		if i == current {
			marker = "✓ "
		}
		// Both markers are two CELLS ("✓ " is four bytes, so not len(marker)).
		name := elideEnd(sanitizeRemoteText(grp.Name), ctxMenuTitleCap-2)
		items = append(items, ctxMenuItem{id: ctxActSetGroup, label: marker + name, enabled: true, groupName: grp.Name})
	}
	items = append(items, ctxMenuItem{id: ctxActNewGroup, label: "  New group…", enabled: true})
	marker := "  "
	if current < 0 {
		marker = "✓ "
	}
	return append(items, ctxMenuItem{id: ctxActUngroup, label: marker + "No group", enabled: true})
}

// openProjectGroupList re-populates the OPEN project menu in place with the
// group list, exactly as openTabColorList does with the colours: same target,
// items replaced, cursor on the current choice, Esc closes the whole menu, the
// box re-derived from its own position, and the menu CLOSED when even the
// re-clamped box cannot fit. Membership is read by (projectDest, projectID).
func (m *Model) openProjectGroupList() {
	cur := m.groups.groupOf(m.ctxMenu.projectDest, m.ctxMenu.projectID)
	s := m.ctxMenu
	s.items = buildProjectGroupItems(m.groups.Groups, cur)
	s.spaced = false
	s.cursor = len(s.items) - 1
	if cur >= 0 {
		s.cursor = cur
	}
	w, h := s.boxSize()
	if w > m.width || h > m.height-2 {
		m.closeCtxMenu()
		return
	}
	s.x, s.y = ctxMenuPos(m.ctxMenu.x-1, m.ctxMenu.y-1, w, h, m.width, m.height)
	m.ctxMenu = s
}

// moveProjectToGroup puts (dest, id) in the group named name, resolved NOW —
// a group renamed, moved or deleted while the list was open is found by name
// or not at all.
func (m *Model) moveProjectToGroup(dest, id, name string) tea.Cmd {
	g := m.groups.indexOf(name)
	if g < 0 || !m.groups.assign(g, dest, id) {
		return nil
	}
	return m.saveGroupsCmd()
}

// ungroupProject takes (dest, id) out of its group.
func (m *Model) ungroupProject(dest, id string) tea.Cmd {
	if !m.groups.unassign(dest, id) {
		return nil
	}
	return m.saveGroupsCmd()
}

// buildGroupCtxMenuItems is the header menu for group g of n. Move up/down
// grey at the ends rather than hide, the pane menu's convention.
func buildGroupCtxMenuItems(g, n int, collapsed bool) []ctxMenuItem {
	toggle := "Collapse"
	if collapsed {
		toggle = "Expand"
	}
	return []ctxMenuItem{
		{id: ctxActRenameGroup, label: "Rename group", enabled: true},
		{id: ctxActToggleGroup, label: toggle, enabled: true},
		{id: ctxActGroupUp, label: "Move up", enabled: g > 0},
		{id: ctxActGroupDown, label: "Move down", enabled: g < n-1},
		{id: ctxActDeleteGroup, label: "Delete group", enabled: true},
	}
}

// openGroupCtxMenu opens the header menu for group g, mirroring
// openTabCtxMenu: it refuses — without mutating anything — while another
// surface owns input, and when even the box cannot fit.
func (m *Model) openGroupCtxMenu(g, anchorX, anchorY int) {
	if g < 0 || g >= len(m.groups.Groups) ||
		m.notesMode || m.renaming || m.renamingPane || m.groupEdit.active() || m.dialog != dialogNone {
		return
	}
	grp := m.groups.Groups[g]
	s := ctxMenuState{
		groupName: grp.Name,
		title:     grp.Name,
		cursor:    -1,
		items:     buildGroupCtxMenuItems(g, len(m.groups.Groups), grp.Collapsed),
	}
	s.cursor = firstEnabled(s.items)
	w, h := s.boxSize()
	if w > m.width || h > m.height-2 {
		return
	}
	m.closeCtxMenu()
	m.clearDragState()
	m.selection = nil
	s.x, s.y = ctxMenuPos(anchorX, anchorY, w, h, m.width, m.height)
	m.ctxMenu = s
}

// executeGroupCtxMenuItem dispatches one header-menu row. The group is
// resolved by NAME at execute time; one that vanished meanwhile does nothing
// (the Update prologue normally closes such a menu first).
func (m Model) executeGroupCtxMenuItem(name string, item ctxMenuItem) (tea.Model, tea.Cmd) {
	m.closeCtxMenu()
	g := m.groups.indexOf(name)
	if g < 0 || !item.enabled {
		return m, nil
	}
	switch item.id {
	case ctxActRenameGroup:
		cur := m.groups.Groups[g].Name
		m.beginGroupEdit(groupEditState{mode: groupEditRename, target: cur, input: cur})
		return m, nil
	case ctxActToggleGroup:
		cmd := m.toggleGroup(g)
		return m, cmd
	case ctxActGroupUp, ctxActGroupDown:
		to := g + 1
		if item.id == ctxActGroupUp {
			to = g - 1
		}
		if !m.groups.moveGroup(g, to) {
			return m, nil
		}
		cmd := m.saveGroupsCmd()
		return m, cmd
	case ctxActDeleteGroup:
		// The group only: its members become ungrouped, nothing is closed.
		m.groups.deleteGroup(g)
		cmd := m.saveGroupsCmd()
		return m, cmd
	}
	return m, nil
}

// groupEditMode says what the group-name editor is for.
type groupEditMode int

const (
	groupEditNone groupEditMode = iota
	groupEditNew
	groupEditRename
)

// groupEditState is the status-bar group-name editor — a sibling of the pane
// rename editor (same place, same ▎ caret), with its own state because that one
// is bound to the active pane and its Enter sends MsgUpdatePane.
type groupEditState struct {
	mode      groupEditMode
	input     string
	target    string // rename: the group's name when the edit opened
	dest      string // new: the project to put in the new group
	projectID string // new: "" creates the group empty
}

func (s groupEditState) active() bool { return s.mode != groupEditNone }

// appendPaste folds pasted text into the editor: printable runes only (a
// paste's line breaks and control bytes are dropped, as in the palette), cut
// at maxGroupNameRunes rather than refused whole like an over-long key.
func (s *groupEditState) appendPaste(text string) {
	room := maxGroupNameRunes - utf8.RuneCountInString(s.input)
	if room <= 0 {
		return
	}
	add := []rune(sanitizePaletteQuery(text))
	if len(add) > room {
		add = add[:room]
	}
	s.input += string(add)
}

// beginGroupEdit opens the editor. The menu that led here is closed and no
// drag survives it.
func (m *Model) beginGroupEdit(s groupEditState) {
	m.closeCtxMenu()
	m.clearDragState()
	m.groupEdit = s
}

// handleGroupEditKey captures every key while the editor is open, like the
// pane rename editor. Text comes from msg.Text through isPrintableText, so a
// name can be non-ASCII; input stops at maxGroupNameRunes.
func (m Model) handleGroupEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "escape":
		m.groupEdit = groupEditState{}
		return m, nil
	case "enter":
		return m.commitGroupEdit()
	case "backspace":
		if r := []rune(m.groupEdit.input); len(r) > 0 {
			m.groupEdit.input = string(r[:len(r)-1])
		}
		return m, nil
	}
	if msg.Text != "" && isPrintableText(msg.Text) &&
		utf8.RuneCountInString(m.groupEdit.input)+utf8.RuneCountInString(msg.Text) <= maxGroupNameRunes {
		m.groupEdit.input += msg.Text
	}
	return m, nil
}

// commitGroupEdit applies the editor. A refused name flashes and leaves the
// editor OPEN with the text kept, so the user fixes it rather than retyping.
func (m Model) commitGroupEdit() (tea.Model, tea.Cmd) {
	e := m.groupEdit
	changed := false
	var err error
	switch e.mode {
	case groupEditNew:
		var g int
		if g, err = m.groups.addGroup(e.input); err == nil {
			changed = true
			if e.projectID != "" {
				m.groups.assign(g, e.dest, e.projectID)
			}
		}
	case groupEditRename:
		// A group deleted while the editor was open has nothing to rename.
		if g := m.groups.indexOf(e.target); g >= 0 {
			if err = m.groups.renameGroup(g, e.input); err == nil {
				changed = true
			}
		}
	}
	if err != nil {
		m.setFlash(groupNameFlash(err))
		return m, m.flashCmd()
	}
	m.groupEdit = groupEditState{}
	if !changed {
		return m, nil
	}
	cmd := m.saveGroupsCmd()
	return m, cmd
}

// groupNameFlash maps a name refusal to its exact flash text.
func groupNameFlash(err error) string {
	if errors.Is(err, errGroupNameTaken) {
		return groupNameTakenFlash
	}
	return groupNameEmptyFlash
}

// toggleActiveProjectGroup (project.group_toggle) collapses or expands the
// active project's group. An ungrouped active project is a silent no-op.
func (m *Model) toggleActiveProjectGroup() tea.Cmd {
	p := m.cur()
	if p == nil {
		return nil
	}
	return m.toggleGroup(m.groups.groupOf(p.Dest, p.ID))
}

// toggleAllGroups (project.groups_collapse_all) collapses every group — or,
// when every group is already collapsed, expands them all.
func (m *Model) toggleAllGroups() tea.Cmd {
	if len(m.groups.Groups) == 0 {
		return nil
	}
	all := true
	for _, grp := range m.groups.Groups {
		if !grp.Collapsed {
			all = false
			break
		}
	}
	for i := range m.groups.Groups {
		m.groups.setCollapsed(i, !all)
	}
	return m.saveGroupsCmd()
}
