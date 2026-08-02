package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
)

// blockedRef pairs a blocked pane with the project and tab it lives in, so a
// caller can jump straight to it without a second lookup.
type blockedRef struct {
	Pane     *PaneModel
	Project  *ProjectModel
	TabIndex int
}

// blockedPanes collects every pane waiting on the user across ALL projects,
// oldest-blocked first. Deliberately not sidebar order: with six agents
// running, the one waiting longest is the one costing you time.
func (m *Model) blockedPanes() []blockedRef {
	var out []blockedRef
	for _, proj := range m.projects {
		for i, tab := range proj.tabs {
			if tab.Root == nil {
				continue
			}
			for _, pane := range tab.Leaves() {
				if pane.blockedSince.IsZero() {
					continue
				}
				out = append(out, blockedRef{Pane: pane, Project: proj, TabIndex: i})
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].Pane.blockedSince.Before(out[b].Pane.blockedSince)
	})
	return out
}

// jumpToNextBlocked moves project, tab, and focus to the longest-blocked
// pane in the workspace. Repeated presses cycle: if the focused pane is
// already in the blocked queue, the jump advances to the NEXT entry rather
// than re-selecting the same pane, so alt+shift+a walks the whole queue.
func (m *Model) jumpToNextBlocked() tea.Cmd {
	blocked := m.blockedPanes()
	if len(blocked) == 0 {
		return nil
	}

	// Advance past the pane we are already on so repeated presses cycle.
	target := blocked[0]
	if cur := m.activeTabModel(); cur != nil {
		if active := cur.ActivePaneModel(); active != nil {
			for i, ref := range blocked {
				if ref.Pane.ID == active.ID {
					target = blocked[(i+1)%len(blocked)]
					break
				}
			}
		}
	}

	for i, p := range m.projects {
		if p == target.Project {
			// Sequenced, not `return m.switchProject(i)` followed by the two
			// field writes: switchProject mutates m through a pointer
			// receiver, and Go does not order a plain operand against a call
			// in the same return statement (see toggleLastProject's identical
			// note in projectpicker.go). Capturing the cmd first, then
			// applying the tab/pane focus, then returning keeps the sequence
			// explicit regardless of how this function is later called.
			cmd := m.switchProject(i)
			target.Project.activeTab = target.TabIndex
			// TabModel.ActivePane is the pane-ID field; ActivePaneModel()
			// repairs a stale value on next read, so assigning it is the
			// whole focus change.
			target.Project.tabs[target.TabIndex].ActivePane = target.Pane.ID
			// MsgSwitchProject only reaches the project's REMEMBERED tab; the
			// blocked pane is routinely in a different one, whose panes are
			// still Pending after a lazy restore. Without this the queue lands
			// the user on a restore indicator with no process behind it.
			m.notifyTabSwitch(target.Project.tabs[target.TabIndex])
			return cmd
		}
	}
	return nil
}
