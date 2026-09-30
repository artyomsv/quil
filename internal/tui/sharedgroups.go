package tui

import (
	"fmt"
	"log"
	"reflect"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Client side of shared groups. m.groups stays the ONE displayed
// structure every sidebar/hover/drag reader indexes, and project-groups.json
// is exactly m.groups: order, collapsed state, and every member. For a
// destination whose frame is authoritative (frameAuthoritativeFor) the file's
// members are a CACHE the frame replaces; for every other destination —
// legacy, not connected yet, or shared but not imported into — they are the
// only record and are shown and kept as before. Nothing is removed from the
// file merely because a destination is shared.

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
//
// It also records which names this frame DROPPED from the destination's list
// (vanishedGroups, consumed by rebuildGroupsView on the same frame). Only a
// destination heard from before this session can drop anything: on its first
// frame there is no previous list, so nothing it omits reads as a delete.
func (m *Model) noteSharedData(msg WorkspaceStateMsg) {
	m.vanishedGroups = nil
	if !msg.SharedData {
		return
	}
	if m.sharedData == nil {
		m.sharedData = map[string]bool{}
		m.daemonGroups = map[string][]string{}
		m.daemonRecent = map[string][]string{}
	}
	// The daemon caps both lists, but a remote host is one the user may not
	// control: keep the first N, as an honest daemon would have sent.
	groups := m.capSharedList(msg.Dest, "groups", msg.Groups, ipc.MaxGroupsPerDaemon)
	recent := m.capSharedList(msg.Dest, "recent folders", msg.RecentCWDs, ipc.MaxRecentCWDs)
	if old, seen := m.daemonGroups[msg.Dest]; seen {
		for _, name := range old {
			if !containsFold(groups, name) {
				m.vanishedGroups = append(m.vanishedGroups, name)
			}
		}
	}
	m.sharedData[msg.Dest] = true
	m.daemonGroups[msg.Dest] = append([]string(nil), groups...)
	m.daemonRecent[msg.Dest] = append([]string(nil), recent...)
}

// capSharedList cuts a frame's list to limit, logging once per destination
// and list: an oversized frame repeats on every broadcast.
func (m *Model) capSharedList(dest, what string, list []string, limit int) []string {
	if len(list) <= limit {
		return list
	}
	key := dest + "\x00" + what
	if !m.sharedCapLogged[key] {
		if m.sharedCapLogged == nil {
			m.sharedCapLogged = map[string]bool{}
		}
		m.sharedCapLogged[key] = true
		log.Printf("shared data: daemon %q sent %d %s, over the cap of %d; keeping the first %d", dest, len(list), what, limit, limit)
	}
	return list[:limit]
}

// frameAuthoritativeFor reports whether dest's frame, not the file, holds its
// group members: it sent shared_data this session AND either its daemon has
// answered this client's groups import (sharedimport.go) or it lists at least
// one group. A shared daemon holding no groups and not yet imported into has
// only the file's members as a record. The second arm covers a client whose
// import is still pending while another client's import already filled the
// daemon: that import will be refused, and the daemon's list is the truth.
func (m *Model) frameAuthoritativeFor(dest string) bool {
	return m.sharedData[dest] && (m.groupsImportAnswered(dest) || len(m.daemonGroups[dest]) > 0)
}

func containsFold(list []string, name string) bool {
	for _, s := range list {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
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
		if containsFold(m.daemonGroups[d], name) {
			out = append(out, d)
		}
	}
	return out
}

// rebuildGroupsView is the merged view, run after every applied
// frame. For each AUTHORITATIVE destination its members are replaced by the
// frame's (each project's own Group); every other destination's members are
// left exactly as the file holds them. Names any shared daemon lists and the
// view lacks are appended in destination order, so the user's order and
// collapsed state stand.
//
// A group is deleted only when its name DISAPPEARED: a destination that
// listed it in its previous frame dropped it in this one (vanishedGroups), no
// destination lists it now, and it has no member left anywhere. So a
// destination's first frame deletes nothing, a name no daemon ever listed is
// kept, and an in-flight frame during an optimistic rename cannot take the
// new name (it was never listed, so it cannot vanish) — while a delete made
// in another client still shows here.
//
// With no shared destination nothing here runs, so a legacy-only setup
// behaves exactly as before. Saves only when the view — which is the file —
// actually changed, not on every frame.
func (m *Model) rebuildGroupsView() tea.Cmd {
	vanished := m.vanishedGroups
	m.vanishedGroups = nil
	if len(m.sharedData) == 0 {
		return nil
	}
	before := m.groups.clone()
	auth := map[string]bool{}
	for d := range m.sharedData {
		if m.frameAuthoritativeFor(d) {
			auth[d] = true
		}
	}
	m.groups = m.groups.withoutMembersOf(auth)
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
		if p == nil || !auth[p.Dest] || p.Group == "" {
			continue
		}
		g := m.groups.indexOf(p.Group)
		if g < 0 {
			var err error
			if g, err = m.groups.addGroup(p.Group); err != nil {
				log.Printf("groups: daemon %q filed project %q under %q: %v; shown ungrouped", p.Dest, p.ID, p.Group, err)
				continue
			}
		}
		m.groups.assign(g, p.Dest, p.ID)
	}
	for g := len(m.groups.Groups) - 1; g >= 0; g-- {
		grp := m.groups.Groups[g]
		if len(grp.Members) == 0 && containsFold(vanished, grp.Name) && len(m.destsListingGroup(grp.Name)) == 0 {
			m.groups.deleteGroup(g)
		}
	}
	// Both sides through clone, which gives an empty group a nil member list
	// however it got there, so an unchanged view compares equal.
	if reflect.DeepEqual(before, m.groups.clone()) {
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
	if !m.groupSendsOpen(dest) {
		// Before the groups import is answered a send could reach the daemon
		// first and make it refuse the import (sharedimport.go). The change
		// is in the file already; the send waits for the answer.
		m.deferGroupOp(dest, msgType, payload, what)
		return nil
	}
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
// local one if it is shared: the local daemon is where empty groups live,
// and sent nowhere the group would reach no other client of any daemon.
// Best-effort across hosts: a refusal flashes, an offline host is skipped,
// and the group shows split until the user repeats the operation.
func (m *Model) sendGroupOpEverywhere(op, name, newName string) tea.Cmd {
	if op == ipc.GroupOpCreate {
		dest := m.activeDest()
		if !m.sharedData[dest] && m.sharedData[""] {
			dest = ""
		}
		return m.sendGroupOp(dest, op, name, newName)
	}
	// A destination whose groups import is unanswered lists nothing yet but
	// will list the name once the import lands: the op is held for it and
	// replayed after the answer (sharedimport.go), or the authoritative frame
	// that follows would undo it.
	targets := m.destsListingGroup(name)
	for _, d := range m.destsHoldingGroupName(name) {
		if !slices.Contains(targets, d) {
			targets = append(targets, d)
		}
	}
	var cmds []tea.Cmd
	for _, dest := range targets {
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
