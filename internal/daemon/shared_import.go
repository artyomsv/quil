package daemon

import (
	"log"
	"path/filepath"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// ImportShared is ONE check-and-apply under sm.mu: groups apply only
// when the daemon has no group list and no grouped project; recent applies
// only when its list is empty. Two clients importing at once therefore
// cannot both apply — the second finds the first's data. Invalid names and
// unknown project ids are skipped, never refused whole.
func (sm *SessionManager) ImportShared(groups []ipc.SharedImportGroup, recent []string, wantGroups, wantRecent bool) (groupsApplied, recentApplied bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if wantGroups {
		empty := len(sm.groups) == 0
		for _, p := range sm.projects {
			if p.Group != "" {
				empty = false
			}
		}
		if empty {
			for _, g := range groups {
				n, err := validateGroupName(g.Name)
				if err != nil || sm.groupIndexLocked(n) >= 0 || len(sm.groups) >= ipc.MaxGroupsPerDaemon {
					continue
				}
				sm.groups = append(sm.groups, n)
				for _, id := range g.ProjectIDs {
					if p, ok := sm.projects[id]; ok {
						p.Group = n
					}
				}
			}
			groupsApplied = true
		}
	}
	if wantRecent && len(sm.recentCWDs) == 0 {
		for _, r := range recent {
			r = strings.TrimSpace(r)
			if r == "" || len(sm.recentCWDs) >= ipc.MaxRecentCWDs {
				continue
			}
			r = filepath.Clean(r)
			dup := false
			for _, have := range sm.recentCWDs {
				if samePath(have, r) {
					dup = true
				}
			}
			if !dup {
				sm.recentCWDs = append(sm.recentCWDs, r)
			}
		}
		recentApplied = true
	}
	return groupsApplied, recentApplied
}

func hasKind(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// handleSharedImport applies a client's old files once. Groups and recent are
// decided synchronously under sm.mu; the notes part writes files, so it runs
// on a worker with each pane's noteMu — a note is imported only while the
// pane has NO note file on disk yet. That is a second, belt-and-braces guard:
// the real one is client-side (sharedimport.go), which never offers a pane
// whose frame already shows a nonzero note_rev, and the rev never resets on a
// delete — so a note a user deleted here cannot be resurrected by an old
// file from another client, and this check only catches the case where the
// daemon itself already holds a file the client does not yet know about.
// The answer is sent when everything asked for is done.
func (d *Daemon) handleSharedImport(conn *ipc.Conn, msg *ipc.Message) {
	if len(msg.Payload) > ipc.MaxSharedImportBytes {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, "import request too large")
		return
	}
	var p ipc.SharedImportPayload
	if err := msg.DecodePayload(&p); err != nil {
		d.replyError(conn, msg, ipc.ErrCodeBadPayload, err.Error())
		return
	}
	wantGroups := hasKind(p.Kinds, ipc.ImportKindGroups)
	wantRecent := hasKind(p.Kinds, ipc.ImportKindRecent)
	wantNotes := hasKind(p.Kinds, ipc.ImportKindNotes)
	resp := ipc.SharedImportRespPayload{}
	resp.GroupsApplied, resp.RecentApplied = d.session.ImportShared(p.Groups, p.Recent, wantGroups, wantRecent)
	if wantGroups {
		resp.Answered = append(resp.Answered, ipc.ImportKindGroups)
	}
	if wantRecent {
		resp.Answered = append(resp.Answered, ipc.ImportKindRecent)
	}
	go func() {
		if wantNotes {
			for _, n := range p.Notes {
				pane := d.session.Pane(n.PaneID)
				if pane == nil || n.Text == "" || len(n.Text) > ipc.MaxNoteBytes {
					resp.NotesSkipped++
					continue
				}
				pane.noteMu.Lock()
				if noteFileExists(pane.ID) {
					pane.noteMu.Unlock()
					resp.NotesSkipped++
					continue
				}
				err := persist.SaveNotes(config.NotesDir(), pane.ID, n.Text)
				if err == nil {
					pane.NoteRev.Store(pane.NoteRev.Load() + 1)
				}
				pane.noteMu.Unlock()
				if err != nil {
					log.Printf("shared_import: note %s: %v", pane.ID, err)
					resp.NotesSkipped++
					continue
				}
				resp.NotesApplied++
			}
			resp.Answered = append(resp.Answered, ipc.ImportKindNotes)
		}
		log.Printf("shared_import: answered=%v groups=%v recent=%v notes=%d/%d",
			resp.Answered, resp.GroupsApplied, resp.RecentApplied, resp.NotesApplied, resp.NotesSkipped)
		respondTo(conn, msg.ID, ipc.MsgSharedImportResp, resp)
		if resp.GroupsApplied || resp.RecentApplied || resp.NotesApplied > 0 {
			d.requestBroadcast()
			d.requestSnapshot()
		}
	}()
}
