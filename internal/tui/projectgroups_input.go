package tui

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	// The same rule the drop-target highlight was painted from, applied to the
	// release row — so the green row is exactly what the release does.
	_, row, ok := m.sidebarDragRows(x, y)
	switch d := m.projectDropFor(idx, row, ok); {
	case d.group != "":
		if !m.groups.assign(m.groups.indexOf(d.group), p.Dest, p.ID) {
			return nil
		}
	case d.ungroup:
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
//
// Unlike the colour list, this one GROWS with the user's data: one row per
// group, so ~18 groups overflow a 24-row terminal. Closing silently there
// reads as a menu row that does nothing, so the close flashes why. (The box is
// at most ctxMenuTitleCap+4 wide, so above minTermWidth it is the HEIGHT that
// fails.) Returns the flash tick, or nil.
func (m *Model) openProjectGroupList() tea.Cmd {
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
		m.setFlash(groupListTooTallFlash)
		return m.flashCmd()
	}
	s.x, s.y = ctxMenuPos(m.ctxMenu.x-1, m.ctxMenu.y-1, w, h, m.width, m.height)
	m.ctxMenu = s
	return nil
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
		m.notesMode || m.renaming || m.renamingPane || m.dialog != dialogNone {
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

// groupEditState backs the group-name dialog (dialogGroupName), used for both
// New group… and Rename group. m.dialog is the open/closed authority; this is
// only its data. It was a status-bar editor first, bottom left beside the pane
// rename one — easy enough to miss that New group… read as doing nothing, while
// it silently held every key and refused every right-click.
type groupEditState struct {
	mode      groupEditMode
	input     string
	target    string // rename: the group's name when the edit opened
	dest      string // new: the project to put in the new group
	projectID string // new: "" creates the group empty
}

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

// beginGroupEdit opens the group-name dialog. The menu that led here is closed
// and no drag survives it; a refusal left over from an earlier open is not
// shown in this one.
func (m *Model) beginGroupEdit(s groupEditState) {
	m.closeCtxMenu()
	m.clearDragState()
	m.clearGroupNameRefusal()
	m.groupEdit = s
	m.dialog = dialogGroupName
}

// closeGroupNameDialog closes the dialog and forgets its data and refusal.
func (m *Model) closeGroupNameDialog() {
	m.dialog = dialogNone
	m.groupEdit = groupEditState{}
	m.clearGroupNameRefusal()
}

// handleGroupEditKey is the dialog's key handler (dispatchDialogKey). Text
// comes from msg.Text through isPrintableText, so a name can be non-ASCII;
// input stops at maxGroupNameRunes. An edit clears a shown refusal, which
// described the text before it.
func (m Model) handleGroupEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "escape":
		m.closeGroupNameDialog()
		return m, nil
	case "enter":
		return m.commitGroupEdit()
	case "backspace":
		if r := []rune(m.groupEdit.input); len(r) > 0 {
			m.groupEdit.input = string(r[:len(r)-1])
			m.clearGroupNameRefusal()
		}
		return m, nil
	}
	if msg.Text != "" && isPrintableText(msg.Text) &&
		utf8.RuneCountInString(m.groupEdit.input)+utf8.RuneCountInString(msg.Text) <= maxGroupNameRunes {
		m.groupEdit.input += msg.Text
		m.clearGroupNameRefusal()
	}
	return m, nil
}

// commitGroupEdit applies the dialog. A refused name flashes — shown INSIDE the
// box, see groupNameRefusal — and leaves the dialog OPEN with the text kept, so
// the user fixes it rather than retyping.
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
	m.closeGroupNameDialog()
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

// groupNameRefusal is the refusal the dialog shows: the flash commitGroupEdit
// set, while it lasts. View draws only the dialog while one is open, so the
// status bar that carries every other flash is not on screen.
func (m Model) groupNameRefusal() string {
	if (m.flashText == groupNameEmptyFlash || m.flashText == groupNameTakenFlash) &&
		time.Now().Before(m.flashUntil) {
		return m.flashText
	}
	return ""
}

// clearGroupNameRefusal drops a group-name refusal flash, and nothing else.
func (m *Model) clearGroupNameRefusal() {
	if m.flashText == groupNameEmptyFlash || m.flashText == groupNameTakenFlash {
		m.flashText = ""
	}
}

// groupNameDialogWidth fits "✗ A group with that name already exists" on
// one line; renderDialog clamps it to a narrow terminal.
const groupNameDialogWidth = 48

// renderGroupNameDialog is the dialog's box content. Every line is cut to the
// inner width: lipgloss wraps an over-wide one, and a 32-rune name can be 64
// cells. The name keeps its TAIL, caret included, and passes sanitizeRemoteText
// at render only — a rename is seeded from project-groups.json. The error row
// is always there, blank without a refusal, so a refusal does not move the box.
func (m Model) renderGroupNameDialog() string {
	inner := dialogInnerWidth(m.width, groupNameDialogWidth)
	title := "New group"
	if m.groupEdit.mode == groupEditRename {
		title = "Rename group"
	}
	var b strings.Builder
	b.WriteString(dialogTitle.Render(truncateToWidth(title, inner)))
	b.WriteString("\n\n")
	// Budgeted on the PLAIN parts before styling — a cut through styled text
	// can split an SGR sequence. The label goes first on a box too narrow for
	// it; the caret always fits (inner is at least 1).
	label := "Name: "
	nameW := inner - lipgloss.Width(label) - 1
	if nameW < 0 {
		label, nameW = "", inner-1
	}
	name := lastCellsToWidth(sanitizeRemoteText(m.groupEdit.input), nameW)
	b.WriteString(dialogNormal.Render(label) + dialogEditStyle.Render(name+"▎"))
	b.WriteString("\n\n")
	if msg := m.groupNameRefusal(); msg != "" {
		b.WriteString(dialogErrorStyle.Render(truncateToWidth("✗ "+msg, inner)))
	}
	b.WriteString("\n\n")
	b.WriteString(dialogSubtle.Render(truncateToWidth("Enter save · Esc cancel", inner)))
	return b.String()
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
