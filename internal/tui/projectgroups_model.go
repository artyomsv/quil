package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"
)

// SetProjectGroups installs the groups cmd/quil loaded and the path every save
// goes to. A setter rather than a read in NewModel, for the reason
// SetRecentCWDs gives: ~46 tests build a Model directly and never set
// QUIL_HOME, and a disk read there would point them at the real ~/.quil. A
// Model that never had this called keeps groupsPath "" — persistence off.
func (m *Model) SetProjectGroups(s ProjectGroupsState, path string) {
	m.groups = s.groups
	m.groupsPath = path
	m.groupsWriter = &groupsWriter{}
}

// projectGroupsSaveFailedMsg is a save Cmd's failure. A LOCAL result, not an
// IPC response: its Update arm must not re-arm listenForMessages.
type projectGroupsSaveFailedMsg struct{}

// saveGroupsCmd persists the groups after a change. The snapshot is cloned
// HERE, on the Update goroutine — the Cmd runs on its own goroutine while
// Update keeps editing m.groups, so handing it the live slices would race and
// write a later state than the one this save was for. groupsSeq orders the
// saves for groupsWriter, and it is bumped even when persistence is off so a
// test can see that a change asked for one.
func (m *Model) saveGroupsCmd() tea.Cmd {
	m.groupsSeq++
	if m.groupsPath == "" || m.groupsWriter == nil {
		return nil
	}
	seq, path, w, snap := m.groupsSeq, m.groupsPath, m.groupsWriter, m.groups.clone()
	return func() tea.Msg {
		if err := w.write(path, seq, snap); err != nil {
			log.Printf("project groups: save: %v", err)
			return projectGroupsSaveFailedMsg{}
		}
		return nil
	}
}

// projectSections splits m.projects into the ungrouped section and one section
// per group, each in m.projects order. It is the ONE definition of which
// section a project is in: the sidebar rows, the within-section drag, the move
// keys and the palette's move rows all read it, so they cannot disagree.
func (m *Model) projectSections() (ungrouped []int, byGroup [][]int) {
	byGroup = make([][]int, len(m.groups.Groups))
	if len(m.groups.Groups) == 0 {
		ungrouped = make([]int, len(m.projects))
		for i := range m.projects {
			ungrouped[i] = i
		}
		return ungrouped, byGroup
	}
	idx := m.groups.memberIndex()
	for i, p := range m.projects {
		if g, ok := idx[groupMember{Dest: p.Dest, ID: p.ID}]; ok {
			byGroup[g] = append(byGroup[g], i)
			continue
		}
		ungrouped = append(ungrouped, i)
	}
	return ungrouped, byGroup
}

// toggleGroup collapses or expands group g and saves.
func (m *Model) toggleGroup(g int) tea.Cmd {
	if g < 0 || g >= len(m.groups.Groups) {
		return nil
	}
	m.groups.setCollapsed(g, !m.groups.Groups[g].Collapsed)
	return m.saveGroupsCmd()
}

// finishGroupDrag ends a press on a group header at the release point (x, y).
// A drag already reordered the groups on motion and only has to save. A press
// that never moved the group is a CLICK only when it is released on that SAME
// header: pressed, dragged off (into the panes, onto a row that reorders
// nothing) and released elsewhere is neither a click nor a reorder, and
// changes nothing. Toggling on the press instead would collapse every group
// the user starts to drag.
func (m *Model) finishGroupDrag(x, y int) tea.Cmd {
	g, moved := m.groupDragIdx, m.groupDragMoved
	m.clearDragState()
	if moved {
		return m.saveGroupsCmd()
	}
	if _, row, ok := m.sidebarDragRows(x, y); !ok || row.kind != sidebarRowGroup || row.index != g {
		return nil
	}
	return m.toggleGroup(g)
}

// pruneProjectGroupsFor drops the members of state.Dest that this broadcast no
// longer describes, and saves. Reached only from the WorkspaceStateMsg arm, and
// only past its destConnected gate — i.e. only for a connected daemon whose
// state actually arrived. Every other destination is left alone, an offline
// one included: its rows are the client's stand-ins, not the daemon's answer.
//
// A broadcast naming NO project prunes nothing. A daemon that speaks projects
// always holds at least one (it bootstraps a Default), so an empty list says
// nothing trustworthy about membership — and acting on it would empty the
// user's groups for that host in one message.
//
// It reads the DAEMON's own list, state.Projects, never broadcastProjects:
// for a daemon that does not speak projects that one synthesises a made-up
// interim project, and pruning against it would drop every real member of
// that destination. Such a broadcast names zero real projects, so it is the
// empty case above.
func (m *Model) pruneProjectGroupsFor(state WorkspaceStateMsg) tea.Cmd {
	if len(m.groups.Groups) == 0 {
		return nil
	}
	infos := state.Projects
	if len(infos) == 0 {
		return nil
	}
	live := make(map[string]bool, len(infos))
	for _, p := range infos {
		live[p.ID] = true
	}
	if !m.groups.prune(map[string]map[string]bool{state.Dest: live}) {
		return nil
	}
	return m.saveGroupsCmd()
}
