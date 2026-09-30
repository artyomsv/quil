package daemon

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/textsafe"
)

// Shared data on the daemon (phase 3b, #237): groups and recent folders.
// Notes live in notes.go, the one-time import in shared_import.go.

var (
	errGroupNameEmpty   = errors.New("group name is empty")
	errGroupNameTooLong = errors.New("group name is longer than 32 characters")
	errGroupNameUnsafe  = errors.New("group name contains a control or bidi character")
	errGroupNameTaken   = errors.New("a group with that name already exists")
	errNoSuchGroup      = errors.New("no such group")
	errTooManyGroups    = errors.New("too many groups")
	errNoSuchProject    = errors.New("no such project")
	errUnknownGroupOp   = errors.New("unknown group op")
)

// validateGroupName is the daemon-side rule: trimmed, 1–32 runes, none of the
// runes the remote-text strip rule would remove. Any IPC client can send a
// name, and the TUI renders every name it receives — so the refusal is here.
func validateGroupName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", errGroupNameEmpty
	}
	if utf8.RuneCountInString(n) > ipc.MaxGroupNameRunes {
		return "", errGroupNameTooLong
	}
	if textsafe.HasStripped(n) {
		return "", errGroupNameUnsafe
	}
	return n, nil
}

// groupIndexLocked is the index of name in sm.groups, case-insensitively, or
// -1. Caller holds sm.mu.
func (sm *SessionManager) groupIndexLocked(name string) int {
	for i, g := range sm.groups {
		if strings.EqualFold(g, name) {
			return i
		}
	}
	return -1
}

// SetProjectGroup files projectID under group ("" ungroups). A name already in
// the list is adopted in the list's own spelling; a new name is appended, so
// the invariant "every non-empty Project.Group is in groups" holds by
// construction. The name is never removed here: an empty group survives.
func (sm *SessionManager) SetProjectGroup(projectID, group string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	p, ok := sm.projects[projectID]
	if !ok {
		return errNoSuchProject
	}
	if strings.TrimSpace(group) == "" {
		p.Group = ""
		return nil
	}
	name, err := validateGroupName(group)
	if err != nil {
		return err
	}
	if i := sm.groupIndexLocked(name); i >= 0 {
		p.Group = sm.groups[i]
		return nil
	}
	if len(sm.groups) >= ipc.MaxGroupsPerDaemon {
		return errTooManyGroups
	}
	sm.groups = append(sm.groups, name)
	p.Group = name
	return nil
}

// GroupOp creates, renames or deletes a group name. Rename follows every
// project filed under the old name; delete ungroups them.
func (sm *SessionManager) GroupOp(op, name, newName string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	switch op {
	case ipc.GroupOpCreate:
		n, err := validateGroupName(name)
		if err != nil {
			return err
		}
		if sm.groupIndexLocked(n) >= 0 {
			return errGroupNameTaken
		}
		if len(sm.groups) >= ipc.MaxGroupsPerDaemon {
			return errTooManyGroups
		}
		sm.groups = append(sm.groups, n)
		return nil
	case ipc.GroupOpRename:
		i := sm.groupIndexLocked(strings.TrimSpace(name))
		if i < 0 {
			return errNoSuchGroup
		}
		n, err := validateGroupName(newName)
		if err != nil {
			return err
		}
		// A case change of its own name is allowed.
		if j := sm.groupIndexLocked(n); j >= 0 && j != i {
			return errGroupNameTaken
		}
		old := sm.groups[i]
		sm.groups[i] = n
		for _, p := range sm.projects {
			if strings.EqualFold(p.Group, old) {
				p.Group = n
			}
		}
		return nil
	case ipc.GroupOpDelete:
		i := sm.groupIndexLocked(strings.TrimSpace(name))
		if i < 0 {
			return errNoSuchGroup
		}
		old := sm.groups[i]
		sm.groups = append(sm.groups[:i:i], sm.groups[i+1:]...)
		for _, p := range sm.projects {
			if strings.EqualFold(p.Group, old) {
				p.Group = ""
			}
		}
		return nil
	default:
		return errUnknownGroupOp
	}
}

// SharedSnapshot returns COPIES of the group list and the recent-folder list.
// A separate RLock from SnapshotState, taken only after that one has
// returned — never nested inside it (the oscillation hazard SnapshotState's
// doc comment names).
func (sm *SessionManager) SharedSnapshot() (groups, recent []string) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.groups) > 0 {
		groups = append([]string(nil), sm.groups...)
	}
	if len(sm.recentCWDs) > 0 {
		recent = append([]string(nil), sm.recentCWDs...)
	}
	return groups, recent
}

// samePath compares two directories the way the client's pathEqual does:
// case-insensitively on Windows, exactly elsewhere. Both are already cleaned.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// RecordRecentCWD moves dir to the front of the recent list, de-duplicated
// and capped at MaxRecentCWDs. A blank dir is ignored.
func (sm *SessionManager) RecordRecentCWD(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	dir = filepath.Clean(dir)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := make([]string, 0, len(sm.recentCWDs)+1)
	out = append(out, dir)
	for _, d := range sm.recentCWDs {
		if samePath(d, dir) {
			continue
		}
		out = append(out, d)
	}
	if len(out) > ipc.MaxRecentCWDs {
		out = out[:ipc.MaxRecentCWDs]
	}
	sm.recentCWDs = out
}

// RestoreShared installs the persisted lists and REPAIRS the invariant: a
// project whose Group names something the list lacks (a hand-edited file, a
// snapshot from a build that wrote one but not the other) gets it appended,
// so set_project_group and the client's merged view never see a grouped
// project under an unlisted name.
func (sm *SessionManager) RestoreShared(groups, recent []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.groups = sm.groups[:0]
	for _, g := range groups {
		if n, err := validateGroupName(g); err == nil && sm.groupIndexLocked(n) < 0 && len(sm.groups) < ipc.MaxGroupsPerDaemon {
			sm.groups = append(sm.groups, n)
		}
	}
	for _, id := range sm.projectOrder {
		p := sm.projects[id]
		if p == nil || p.Group == "" {
			continue
		}
		if i := sm.groupIndexLocked(p.Group); i >= 0 {
			p.Group = sm.groups[i]
			continue
		}
		if n, err := validateGroupName(p.Group); err == nil && len(sm.groups) < ipc.MaxGroupsPerDaemon {
			sm.groups = append(sm.groups, n)
			p.Group = n
		} else {
			p.Group = ""
		}
	}
	sm.recentCWDs = sm.recentCWDs[:0]
	for _, r := range recent {
		if strings.TrimSpace(r) == "" || len(sm.recentCWDs) >= ipc.MaxRecentCWDs {
			continue
		}
		sm.recentCWDs = append(sm.recentCWDs, filepath.Clean(r))
	}
}

// handleSetProjectGroup answers with project_op_resp (answerOp — an id-less
// send gets nothing, like every project mutation) and broadcasts through the
// coalescer on success.
func (d *Daemon) handleSetProjectGroup(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.SetProjectGroupPayload
	if err := msg.DecodePayload(&p); err != nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	if err := d.session.SetProjectGroup(p.ProjectID, p.Group); err != nil {
		log.Printf("set project group %s %q: %v", p.ProjectID, p.Group, err)
		answerOp(conn, msg, ipc.MsgProjectOpResp, p.ProjectID, false, err.Error())
		return
	}
	answerOp(conn, msg, ipc.MsgProjectOpResp, p.ProjectID, true, "")
	d.requestBroadcast()
	d.requestSnapshot()
}

func (d *Daemon) handleGroupOp(conn *ipc.Conn, msg *ipc.Message) {
	var p ipc.GroupOpPayload
	if err := msg.DecodePayload(&p); err != nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	if err := d.session.GroupOp(p.Op, p.Name, p.NewName); err != nil {
		log.Printf("group op %s %q→%q: %v", p.Op, p.Name, p.NewName, err)
		answerOp(conn, msg, ipc.MsgGroupOpResp, p.Name, false, fmt.Sprintf("%s: %v", p.Op, err))
		return
	}
	answerOp(conn, msg, ipc.MsgGroupOpResp, p.Name, true, "")
	d.requestBroadcast()
	d.requestSnapshot()
}

// resolveRequestedCWDRecording is resolveRequestedCWD plus the recent-folder
// record: the request's own directory is recorded exactly when it resolved.
// A defaulted CWD (empty request) and an unusable one are never recorded.
func (d *Daemon) resolveRequestedCWDRecording(cwd, fallback string) string {
	if cwd == "" {
		return fallback
	}
	if dir := resolveSpawnDirWithin(cwd, spawnDirProbeTimeout); dir != "" {
		d.session.RecordRecentCWD(dir)
		return dir
	}
	log.Printf("spawn cwd: rejecting %q (missing, not a directory, or did not answer in time); using %q", cwd, fallback)
	return fallback
}

// recordRequestedCWD records a browsed directory for a create whose pane will
// NOT be spawned there — a worktree create, whose pane lands in the new
// checkout. The client recorded the browsed directory in that case too, and
// the checkout path is not somewhere the user picks again.
func (d *Daemon) recordRequestedCWD(cwd string) {
	if cwd == "" {
		return
	}
	if dir := resolveSpawnDirWithin(cwd, spawnDirProbeTimeout); dir != "" {
		d.session.RecordRecentCWD(dir)
	}
}

// stringList reads a JSON string array out of the map-based restore.
func stringList(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
