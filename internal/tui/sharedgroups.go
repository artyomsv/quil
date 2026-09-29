package tui

import (
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Client side of shared groups (spec 4.2). m.groups stays the ONE displayed
// structure every sidebar/hover/drag reader indexes; this file rebuilds its
// membership from each shared destination's frame and keeps the file to
// order + collapsed (+ legacy members).

type pendingGroupOp struct {
	dest string
	what string
}

// sharedOpRespMsg is a project_op_resp/group_op_resp for an id this client
// sent. An IPC response: its Update arm re-arms listenForMessages.
type sharedOpRespMsg struct {
	dest string
	id   string
	resp ipc.OpRespPayload
}

// noteSharedData records a frame's shared-data facts for its destination.
// Runs BEFORE applyWorkspaceState so the import (sharedimport.go) and the
// merged view see them on the same frame.
func (m *Model) noteSharedData(msg WorkspaceStateMsg) {
	if !msg.SharedData {
		return
	}
	if m.sharedData == nil {
		m.sharedData = map[string]bool{}
		m.daemonGroups = map[string][]string{}
		m.daemonRecent = map[string][]string{}
	}
	m.sharedData[msg.Dest] = true
	m.daemonGroups[msg.Dest] = append([]string(nil), msg.Groups...)
	m.daemonRecent[msg.Dest] = append([]string(nil), msg.RecentCWDs...)
}

// sharedDestsInOrder is every shared destination, local first, then sorted —
// the order daemon-listed names are appended to the file in.
func (m *Model) sharedDestsInOrder() []string {
	var out []string
	if m.sharedData[""] {
		out = append(out, "")
	}
	var rest []string
	for d, on := range m.sharedData {
		if on && d != "" {
			rest = append(rest, d)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// destsListingGroup is every shared destination whose daemon lists name
// (case-insensitively) — the fan-out for a rename or delete.
func (m *Model) destsListingGroup(name string) []string {
	var out []string
	for _, d := range m.sharedDestsInOrder() {
		for _, g := range m.daemonGroups[d] {
			if strings.EqualFold(g, name) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// rebuildGroupsView is the merged view (spec 4.2), run after every applied
// frame: members of shared destinations come from the frame (the project's
// own Group), names any shared daemon lists are appended in destination
// order, and — only when the LOCAL daemon is shared, since that is where
// empty groups live — a file group with no member that no daemon lists is
// dropped. With no shared destination at all nothing here runs, so a
// legacy-only setup behaves exactly as before. Saves only when the FILE
// projection changed, not on every frame.
func (m *Model) rebuildGroupsView() tea.Cmd {
	if len(m.sharedData) == 0 {
		return nil
	}
	before := m.groups.withoutMembersOf(m.sharedData)
	for g := range m.groups.Groups {
		kept := m.groups.Groups[g].Members[:0]
		for _, mb := range m.groups.Groups[g].Members {
			if !m.sharedData[mb.Dest] {
				kept = append(kept, mb)
			}
		}
		m.groups.Groups[g].Members = kept
	}
	for _, dest := range m.sharedDestsInOrder() {
		for _, name := range m.daemonGroups[dest] {
			if m.groups.indexOf(name) < 0 {
				if _, err := m.groups.addGroup(name); err != nil {
					log.Printf("groups: daemon %q listed %q: %v", dest, name, err)
				}
			}
		}
	}
	for _, p := range m.projects {
		if p == nil || !m.sharedData[p.Dest] || p.Group == "" {
			continue
		}
		g := m.groups.indexOf(p.Group)
		if g < 0 {
			var err error
			if g, err = m.groups.addGroup(p.Group); err != nil {
				continue
			}
		}
		m.groups.assign(g, p.Dest, p.ID)
	}
	if m.sharedData[""] {
		for g := len(m.groups.Groups) - 1; g >= 0; g-- {
			grp := m.groups.Groups[g]
			if len(grp.Members) == 0 && len(m.destsListingGroup(grp.Name)) == 0 {
				m.groups.deleteGroup(g)
			}
		}
	}
	if reflect.DeepEqual(before, m.groups.withoutMembersOf(m.sharedData)) {
		return nil
	}
	return m.saveGroupsCmd()
}

// sharedOpErrCap bounds the daemon's refusal text in the flash: it comes from
// a host the user may not control, and sanitizing removes escapes without
// shortening anything.
const sharedOpErrCap = 80

// sendSharedOp sends one id-bearing group message to dest, recording the id
// so the answer can be matched. Synchronous on the Update goroutine (the
// Clear-attention precedent): a one-shot the user asked for, never a bulk
// iterator. Returns a flash Cmd when the destination is unreachable.
func (m *Model) sendSharedOp(dest, msgType string, payload any, what string) tea.Cmd {
	msg, err := ipc.NewMessage(msgType, payload)
	if err != nil {
		log.Printf("groups: encode %s: %v", msgType, err)
		return nil
	}
	msg.ID = "grp-" + m.nextReqGen()
	if m.pendingGroupOps == nil {
		m.pendingGroupOps = map[string]pendingGroupOp{}
	}
	m.pendingGroupOps[msg.ID] = pendingGroupOp{dest: dest, what: what}
	if err := m.sendForDestStrict(dest, msg); err != nil {
		delete(m.pendingGroupOps, msg.ID)
		m.setFlash(fmt.Sprintf("%s: %s not sent — %v", hostLabel(dest), what, err))
		return m.flashCmd()
	}
	return nil
}

// sendSetProjectGroup files (or ungroups, group "") a project on its own
// daemon; nothing for a legacy destination, whose members live in the file.
func (m *Model) sendSetProjectGroup(dest, projectID, group string) tea.Cmd {
	if !m.sharedData[dest] {
		return nil
	}
	return m.sendSharedOp(dest, ipc.MsgSetProjectGroup, ipc.SetProjectGroupPayload{ProjectID: projectID, Group: group}, "group change")
}

func (m *Model) sendGroupOp(dest, op, name, newName string) tea.Cmd {
	if !m.sharedData[dest] {
		return nil
	}
	return m.sendSharedOp(dest, ipc.MsgGroupOp, ipc.GroupOpPayload{Op: op, Name: name, NewName: newName}, op+" group")
}

// sendGroupOpEverywhere fans a rename or delete out to every shared
// destination listing the name (each with its own id); a create goes to the
// active destination only — or, when that one is a legacy daemon, to the
// local one if it is shared: an empty group nobody lists is pruned by the
// next local frame (rebuildGroupsView), and the local daemon is where empty
// groups live (spec 4.5). Best-effort across hosts (spec 4.2): a refusal
// flashes, an offline host is skipped, and the group shows split until the
// user repeats the operation.
func (m *Model) sendGroupOpEverywhere(op, name, newName string) tea.Cmd {
	if op == ipc.GroupOpCreate {
		dest := m.activeDest()
		if !m.sharedData[dest] && m.sharedData[""] {
			dest = ""
		}
		return m.sendGroupOp(dest, op, name, newName)
	}
	var cmds []tea.Cmd
	for _, dest := range m.destsListingGroup(name) {
		if !m.destConnected(dest) {
			continue
		}
		cmds = append(cmds, m.sendGroupOp(dest, op, name, newName))
	}
	return tea.Batch(cmds...)
}

// applySharedOpResp flashes a refusal naming the host. The optimistic local
// change is undone by that daemon's next frame, not here.
func (m *Model) applySharedOpResp(msg sharedOpRespMsg) tea.Cmd {
	// An answer from a daemon other than the one asked is not the answer: ids
	// are this client's counter, and one host must not settle another's op.
	op, ok := m.pendingGroupOps[msg.id]
	if !ok || op.dest != msg.dest {
		return nil
	}
	delete(m.pendingGroupOps, msg.id)
	if msg.resp.OK {
		return nil
	}
	// Filed against the destination the request went to (pendingGroupOps),
	// never the answer's own Origin: the id is what this client minted.
	reason := elideEnd(sanitizeRemoteText(msg.resp.Error), sharedOpErrCap)
	m.setFlash(fmt.Sprintf("%s: %s refused — %s", hostLabel(op.dest), op.what, reason))
	return m.flashCmd()
}

// recentListFor is the Ctrl+N recent list for dest: the daemon's for a shared
// destination, the client file's otherwise.
func (m *Model) recentListFor(dest string) []string {
	if m.sharedData[dest] {
		return m.daemonRecent[dest]
	}
	return m.recentCWDs
}
