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
	// rename is the rename this op's request id belongs to, when it is one
	// (sendSharedOpWith). Answers are matched by that id, never by names.
	rename *groupRename
}

// groupRename is one group rename this client sent, from the commit until
// every daemon it went to has settled it. The DAEMON settles it, never a
// guess: its refusal, its accept (whose alias lasts until its list shows
// the rename, see renameAccepted), or — when the answer was lost with the
// link — the first frame it sends afterwards.
//
// The view shows the new name from the commit on, on the same group, so its
// order and collapsed state stay. Its origin is not touched. Until a daemon
// settles, rebuildGroupsView reads that daemon's list and its projects'
// groups with the old name replaced by the new (aliasedGroupLists): a frame
// sent before the daemon applied the rename must not drop the new name or
// add the old one back.
//
// A rename held behind a groups import (deferGroupOp) keeps its entry: the
// held op carries it, and the replayed op's request id is filed with it. A
// host that leaves this client (disconnectDest) settles its part at once
// (leaveGroupRenames); a host whose link is lost decides by its next frame.
type groupRename struct {
	oldName, newName string
	dests            map[string]renameState
	// accepted: a daemon settled it as taken — its list showed the rename,
	// or it left this client after its OK. Never put the old name back then.
	// An OK alone is only the destination's renameAccepted state: a link
	// lost before the list confirms it turns it back into renameLost.
	accepted bool
}

type renameState int

const (
	renameWaiting   renameState = iota // the answer can still arrive
	renameLost                         // it cannot; the daemon's next frame decides
	renameFrameSeen                    // that frame arrived (noteSharedData)
	// renameAccepted: the daemon said OK, but it answers before its
	// coalesced broadcast, and a snapshot it built BEFORE the rename can
	// still be broadcast after the OK (buildWorkspaceState releases its lock
	// before sending — the pane_seen revision-barrier comment in
	// internal/daemon/daemon.go). So the alias is retired by CONTENT, not by
	// the next frame: only once that daemon's list shows the rename
	// (settleRenamesFromFrames). Until then another host's frame, or the
	// daemon's own stale one, would re-add the old name and delete the
	// renamed group, losing its place and collapsed state. This holds for
	// the OK's own connection only; a lost link turns it into renameLost.
	renameAccepted
)

// acceptGroupRename records dest's OK. dest's alias is retired, and the
// rename settled as accepted, once its list shows the rename
// (settleRenamesFromFrames).
//
// The OK guards only frames on the connection it came on. When that link is
// lost first (acceptedRenamesLost), the daemon's truth may have moved on — a
// crash restoring a pre-rename snapshot, another client renaming back — so
// the first frame on the new connection decides, as for a lost reply.
func (m *Model) acceptGroupRename(r *groupRename, dest string) {
	if r == nil {
		return
	}
	if _, in := r.dests[dest]; in {
		r.dests[dest] = renameAccepted
	}
}

// acceptedRenamesLost turns dest's unconfirmed OKs into lost answers when
// its link goes (forgetImportFor).
func (m *Model) acceptedRenamesLost(dest string) {
	for _, r := range m.groupRenames {
		if st, in := r.dests[dest]; in && st == renameAccepted {
			r.dests[dest] = renameLost
		}
	}
}

// trackGroupRename records a rename about to go to every daemon the fan-out
// reaches (sendGroupOpEverywhere's targets) and returns it. With none, the
// name is this client's alone and there is nothing to send or settle: nil.
func (m *Model) trackGroupRename(oldName, newName string) *groupRename {
	r := &groupRename{oldName: oldName, newName: newName, dests: map[string]renameState{}}
	for _, d := range m.groupOpTargets(oldName) {
		if m.sharedData[d] && m.destConnected(d) {
			r.dests[d] = renameWaiting
		}
	}
	if len(r.dests) == 0 {
		return nil
	}
	m.groupRenames = append(m.groupRenames, r)
	return r
}

// pendingRenameOf is the unsettled rename that gave group name its current
// name, or nil. One group has at most one: a second rename is refused until
// the first settles (commitGroupEdit), so an answer can only be for the one
// request its id names.
func (m *Model) pendingRenameOf(name string) *groupRename {
	for _, r := range m.groupRenames {
		if strings.EqualFold(r.newName, name) {
			return r
		}
	}
	return nil
}

// renameWaitingFlash names the hosts a pending rename still waits for.
func (m *Model) renameWaitingFlash(r *groupRename) string {
	var hosts []string
	for _, d := range m.sharedDestsInOrder() {
		if _, in := r.dests[d]; in {
			hosts = append(hosts, hostLabel(d))
		}
	}
	return "rename still waiting for " + strings.Join(hosts, ", ")
}

// sendGroupRename sends r to each daemon it tracks. Each op gets its own
// request id (sendSharedOpWith), which alone matches its answer to r — a
// held op carries r into the replay (deferredGroupOp.rename).
func (m *Model) sendGroupRename(r *groupRename) tea.Cmd {
	var cmds []tea.Cmd
	for _, d := range m.sharedDestsInOrder() {
		if _, in := r.dests[d]; !in {
			continue
		}
		m.recordGroupNameSent(d, ipc.GroupOpRename, r.oldName, r.newName)
		cmds = append(cmds, m.sendSharedOpWith(d, ipc.MsgGroupOp,
			ipc.GroupOpPayload{Op: ipc.GroupOpRename, Name: r.oldName, NewName: r.newName}, ipc.GroupOpRename+" group", r))
	}
	return tea.Batch(cmds...)
}

// leaveGroupRenames settles dest's part of every pending rename when the
// host leaves this client (disconnectDest): no frame from it can come to
// decide, so its answer stands as it is — its OK as an accept, anything
// else as not accepted — and a rename with no host left resolves from the
// answers it has. Runs before forgetImportFor, which would turn the OK into
// a lost answer.
func (m *Model) leaveGroupRenames(dest string) tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range slices.Clone(m.groupRenames) {
		cmds = append(cmds, m.settleGroupRename(r, dest, r.dests[dest] == renameAccepted))
	}
	return tea.Batch(cmds...)
}

// settleGroupRename records dest's outcome. When it was the last daemon and
// none accepted, the same group gets its old name back.
func (m *Model) settleGroupRename(r *groupRename, dest string, accepted bool) tea.Cmd {
	if r == nil {
		return nil
	}
	if _, in := r.dests[dest]; !in {
		return nil
	}
	delete(r.dests, dest)
	r.accepted = r.accepted || accepted
	if len(r.dests) > 0 {
		return nil
	}
	m.groupRenames = slices.DeleteFunc(m.groupRenames, func(x *groupRename) bool { return x == r })
	if r.accepted {
		return nil
	}
	g := m.groups.indexOf(r.newName)
	if g < 0 {
		return nil // renamed again or deleted since: that edit stands
	}
	if err := m.groups.renameGroup(g, r.oldName); err != nil {
		log.Printf("groups: rename %q -> %q refused, and %q cannot be put back: %v", r.oldName, r.newName, r.oldName, err)
		return nil
	}
	return m.saveGroupsCmd()
}

// settleRenamesFromFrames settles every rename that a daemon's latest list
// already answers. The new name listed is an accept: the daemon refuses a
// rename onto a name it holds (groupOp in internal/daemon/shared.go), so no
// snapshot from before the rename can list it. After the daemon's OK, a list
// with NEITHER name retires the alias too — the group was renamed and then
// deleted, and only a pre-rename snapshot still lists the old name, which
// keeps it. After a lost answer, the old name listed is a refusal, and
// neither name leaves the view as it is.
//
// The new name alone decides, even when the old one is listed beside it:
// that can only be a group created under the old name AFTER the rename, and
// keeping the alias then would file its projects under the new name.
func (m *Model) settleRenamesFromFrames() {
	for _, r := range slices.Clone(m.groupRenames) {
		for d, st := range r.dests {
			list := m.daemonGroups[d]
			switch {
			case containsFold(list, r.newName),
				st == renameAccepted && !containsFold(list, r.oldName):
				m.settleGroupRename(r, d, true)
			case st != renameFrameSeen:
			case containsFold(list, r.oldName):
				m.settleGroupRename(r, d, false)
			default:
				m.settleGroupRename(r, d, true)
			}
		}
	}
}

// aliasedGroupLists is every shared destination's list as rebuildGroupsView
// reads it: each unsettled rename sent there replaces its old name with the
// new one, in the order the renames were made.
func (m *Model) aliasedGroupLists() map[string][]string {
	out := make(map[string][]string, len(m.daemonGroups))
	for d, list := range m.daemonGroups {
		out[d] = list
	}
	for _, r := range m.groupRenames {
		for d := range r.dests {
			list := out[d]
			if !containsFold(list, r.oldName) || containsFold(list, r.newName) {
				continue
			}
			list = slices.Clone(list)
			for i, n := range list {
				if strings.EqualFold(n, r.oldName) {
					list[i] = r.newName
				}
			}
			out[d] = list
		}
	}
	return out
}

// aliasedGroup is a project's group as rebuildGroupsView reads it: through
// every unsettled rename sent to its daemon.
func (m *Model) aliasedGroup(dest, group string) string {
	for _, r := range m.groupRenames {
		if _, in := r.dests[dest]; in && strings.EqualFold(group, r.oldName) {
			group = r.newName
		}
	}
	return group
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
	groups := canonicalGroupList(m.capSharedList(msg.Dest, "groups", msg.Groups, ipc.MaxGroupsPerDaemon))
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
	// A rename whose answer was lost is decided by this frame: it was sent
	// after the daemon read the rename, if the daemon ever did.
	for _, r := range m.groupRenames {
		if st, in := r.dests[msg.Dest]; in && st == renameLost {
			r.dests[msg.Dest] = renameFrameSeen
		}
	}
	m.daemonRecent[msg.Dest] = append([]string(nil), recent...)
}

// canonicalGroupList is a daemon's group list in the one identity every group
// lookup uses: each name through normalizeGroupName (what indexOf compares),
// blanks dropped, and a name equal to an earlier one ignoring case dropped. An
// honest daemon's names already are canonical (validateGroupName); a host's
// that are not must not reach the view, the guard or the vanished check as a
// second spelling of one group.
func canonicalGroupList(list []string) []string {
	var out []string
	for _, s := range list {
		if n := normalizeGroupName(s); n != "" && !containsFold(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// capSharedList cuts a frame's list to limit, logging once per destination
// and list: an oversized frame repeats on every broadcast.
func (m *Model) capSharedList(dest, what string, list []string, limit int) []string {
	if len(list) <= limit {
		return list
	}
	if m.firstSharedCapHit(dest, what) {
		log.Printf("shared data: daemon %q sent %d %s, over the cap of %d; keeping the first %d", dest, len(list), what, limit, limit)
	}
	return list[:limit]
}

// firstSharedCapHit reports whether dest broke the limit what for the first
// time this session, so each limit logs once per destination.
func (m *Model) firstSharedCapHit(dest, what string) bool {
	key := dest + "\x00" + what
	if m.sharedCapLogged[key] {
		return false
	}
	if m.sharedCapLogged == nil {
		m.sharedCapLogged = map[string]bool{}
	}
	m.sharedCapLogged[key] = true
	return true
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
// Every name here is in ONE identity: normalizeGroupName, compared ignoring
// case — what indexOf uses. Daemon lists (canonicalGroupList) and each
// project's Group (parseWorkspaceState) are canonicalised on intake, so a
// lookup, a claim, the join rule and the vanished check cannot disagree about
// which group a spelling names.
//
// Provenance (projectGroup.Origin/Hosts, saved in the file): every shared
// destination's list CLAIMS the names it carries, and an authoritative
// destination's claim on a name it no longer lists is dropped. A group is the
// user's (userOwned) when created here, or a legacy group from a version 1
// file; a group a daemon's list added is a host group. A rename keeps the
// origin: a host's group renamed here is still the host's.
//
// A project of an authoritative destination JOINS a group only when its own
// daemon lists that name in THIS frame, or the group is the user's. Nothing
// else joins — not a name another host lists, not one a daemon listed before.
// A daemon keeps every name its projects carry in its own list
// (SetProjectGroup, GroupOp, ImportShared, and RestoreShared's repair), so an
// honest host loses nothing; any other project is shown ungrouped, and its
// daemon is logged once. A host therefore puts at most its capped list in the
// view, and that holds across restarts because the claims are saved.
//
// A group with no member anywhere that no daemon lists now is deleted when it
// is a host group no destination claims any more — on that host's
// authoritative frame, even when an earlier launch cached it — or when its
// name DISAPPEARED: a destination that listed it in its previous frame dropped
// it in this one (vanishedGroups), which is how a delete made in another
// client shows here. A user group nobody ever listed is never deleted, and an
// in-flight frame during a rename cannot take the new name: until the daemon
// settles the rename, its list is read with the new name in place of the old
// (aliasedGroupLists, groupRename).
// A group that still has members is never deleted.
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
	// After the snapshot, so a rename a frame refused is saved with the rest.
	m.settleRenamesFromFrames()
	lists := m.aliasedGroupLists()
	auth := map[string]bool{}
	for d := range m.sharedData {
		if m.frameAuthoritativeFor(d) {
			auth[d] = true
		}
	}
	m.groups = m.groups.withoutMembersOf(auth)
	for _, dest := range m.sharedDestsInOrder() {
		for _, name := range lists[dest] {
			g := m.groups.indexOf(name)
			if g < 0 {
				var err error
				if g, err = m.groups.addGroup(name); err != nil {
					log.Printf("groups: daemon %q listed %q: %v", dest, name, err)
					continue
				}
				m.groups.Groups[g].Origin = groupOriginHost
			}
			m.groups.claim(g, dest)
		}
		if auth[dest] {
			m.groups.dropClaimsNotIn(dest, lists[dest])
		}
	}
	for _, p := range m.projects {
		if p == nil || !auth[p.Dest] || p.Group == "" {
			continue
		}
		group := m.aliasedGroup(p.Dest, p.Group)
		g := m.groups.indexOf(group)
		if g < 0 || !(m.groups.Groups[g].userOwned() || containsFold(lists[p.Dest], group)) {
			if m.firstSharedCapHit(p.Dest, "unlisted project group") {
				log.Printf("groups: daemon %q filed project %q under %q, a name it does not list; shown ungrouped", p.Dest, p.ID, p.Group)
			}
			continue
		}
		m.groups.assign(g, p.Dest, p.ID)
	}
	for g := len(m.groups.Groups) - 1; g >= 0; g-- {
		grp := m.groups.Groups[g]
		if len(grp.Members) > 0 || len(m.destsListingGroup(grp.Name)) > 0 {
			continue
		}
		if (!grp.userOwned() && len(grp.Hosts) == 0) || containsFold(vanished, grp.Name) {
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
	return m.sendSharedOpWith(dest, msgType, payload, what, nil)
}

// sendSharedOpWith is sendSharedOp for one op of rename rn (nil for any
// other op): the op's request id is filed with rn, so its answer settles rn.
func (m *Model) sendSharedOpWith(dest, msgType string, payload any, what string, rn *groupRename) tea.Cmd {
	if !m.groupSendsOpen(dest) {
		// Before the groups import is answered a send could reach the daemon
		// first and make it refuse the import (sharedimport.go). The change
		// is in the file already; the send waits for the answer.
		m.deferGroupOp(dest, msgType, payload, what, rn)
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
	m.pendingGroupOps[msg.ID] = pendingGroupOp{dest: dest, what: what, rename: rn}
	if err := m.sendForDestStrict(dest, msg); err != nil {
		delete(m.pendingGroupOps, msg.ID)
		m.setErrorFlash(fmt.Sprintf("%s: %s not sent — %v", hostLabel(dest), what, err))
		// Not sent is an answer: that daemon has the old name.
		return tea.Batch(m.settleGroupRename(rn, dest, false), m.flashCmd())
	}
	return nil
}

// sendSetProjectGroup files (or ungroups, group "") a project on its own
// daemon; nothing for a legacy destination, whose members live in the file.
func (m *Model) sendSetProjectGroup(dest, projectID, group string) tea.Cmd {
	if !m.sharedData[dest] {
		return nil
	}
	// The daemon creates a group it does not have yet, so a name first
	// named here is a create for the follow-up targets too.
	if group != "" && !containsFold(m.daemonGroups[dest], group) {
		m.recordGroupNameSent(dest, ipc.GroupOpCreate, group, "")
	}
	return m.sendSharedOp(dest, ipc.MsgSetProjectGroup, ipc.SetProjectGroupPayload{ProjectID: projectID, Group: group}, "group change")
}

func (m *Model) sendGroupOp(dest, op, name, newName string) tea.Cmd {
	if !m.sharedData[dest] {
		return nil
	}
	m.recordGroupNameSent(dest, op, name, newName)
	return m.sendSharedOp(dest, ipc.MsgGroupOp, ipc.GroupOpPayload{Op: op, Name: name, NewName: newName}, op+" group")
}

// recordGroupNameSent keeps, per destination, the group names this client's
// own create and rename sends put there. A daemon's list comes only from its
// frames, so a name just created or renamed to is not in it until the next
// frame; a rename or delete of that name made before then must still go to
// that destination, after the op that made it (sends to one daemon stay in
// order). A delete, or a rename away, removes the name again. A REFUSAL
// removes nothing: another op still in flight (or already answered) may have
// created the same name, and the answers cannot tell which one did. So a
// wrong entry only ever ADDS a target — one more op the daemon refuses, with
// a flash — never drops one, which would lose the user's edit silently. It is
// never used to show or delete a group.
func (m *Model) recordGroupNameSent(dest, op, name, newName string) {
	if m.groupNamesSent == nil {
		m.groupNamesSent = map[string][]string{}
	}
	drop := func(n string) {
		m.groupNamesSent[dest] = slices.DeleteFunc(m.groupNamesSent[dest], func(s string) bool { return strings.EqualFold(s, n) })
	}
	add := func(n string) {
		if !containsFold(m.groupNamesSent[dest], n) {
			m.groupNamesSent[dest] = append(m.groupNamesSent[dest], n)
		}
	}
	switch op {
	case ipc.GroupOpCreate:
		add(name)
	case ipc.GroupOpRename:
		drop(name)
		add(newName)
	case ipc.GroupOpDelete:
		drop(name)
	}
}

// groupCreateDest is where a group created with no project goes: the active
// destination, or the local one when the active daemon is a legacy one and
// the local daemon is shared.
func (m *Model) groupCreateDest() string {
	dest := m.activeDest()
	if !m.sharedData[dest] && m.sharedData[""] {
		dest = ""
	}
	return dest
}

// groupOpTargets is every destination a rename or delete of name goes to.
func (m *Model) groupOpTargets(name string) []string {
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
	// A name this client created or renamed to is not in the daemon's list
	// until its next frame; the op still goes there, after the first.
	for _, d := range m.sharedDestsInOrder() {
		if containsFold(m.groupNamesSent[d], name) && !slices.Contains(targets, d) {
			targets = append(targets, d)
		}
	}
	return targets
}

// groupTouchesReadOnly reports whether renaming or deleting group name would
// change a read-only destination: a member project lives there, or the
// fan-out names it. A viewer cannot send either change, and making it only in
// this client's file would leave the group split from the daemon's.
func (m *Model) groupTouchesReadOnly(name string) bool {
	if g := m.groups.indexOf(name); g >= 0 {
		for _, mem := range m.groups.Groups[g].Members {
			if m.destReadOnly(mem.Dest) {
				return true
			}
		}
	}
	for _, d := range m.groupOpTargets(name) {
		if m.destReadOnly(d) {
			return true
		}
	}
	return false
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
		return m.sendGroupOp(m.groupCreateDest(), op, name, newName)
	}
	var cmds []tea.Cmd
	for _, dest := range m.groupOpTargets(name) {
		if !m.destConnected(dest) {
			continue
		}
		cmds = append(cmds, m.sendGroupOp(dest, op, name, newName))
	}
	return tea.Batch(cmds...)
}

// applySharedOpResp flashes a refusal naming the host. The optimistic local
// change is undone by that daemon's next frame, not here — except a rename,
// which the answer settles (settleGroupRename).
func (m *Model) applySharedOpResp(msg sharedOpRespMsg) tea.Cmd {
	// An answer from a daemon other than the one asked is not the answer: ids
	// are this client's counter, and one host must not settle another's op.
	op, ok := m.pendingGroupOps[msg.id]
	if !ok || op.dest != msg.dest {
		return nil
	}
	delete(m.pendingGroupOps, msg.id)
	if msg.resp.OK {
		m.acceptGroupRename(op.rename, op.dest)
		return nil
	}
	settle := m.settleGroupRename(op.rename, op.dest, false)
	// Filed against the destination the request went to (pendingGroupOps),
	// never the answer's own Origin: the id is what this client minted.
	reason := elideEnd(sanitizeRemoteText(msg.resp.Error), sharedOpErrCap)
	m.setErrorFlash(fmt.Sprintf("%s: %s refused — %s", hostLabel(op.dest), op.what, reason))
	return tea.Batch(settle, m.flashCmd())
}

// recentListFor is the Ctrl+N recent list for dest: the daemon's for a shared
// destination, the client file's otherwise.
func (m *Model) recentListFor(dest string) []string {
	if m.sharedData[dest] {
		return m.daemonRecent[dest]
	}
	return m.recentCWDs
}
