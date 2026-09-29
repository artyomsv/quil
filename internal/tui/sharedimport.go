package tui

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// One-time import of the client's old files into each shared daemon
// (spec 4.5). Automatic on the first shared frame from a destination, once
// per launch; the marker records what each daemon ANSWERED, so a kind that
// got no answer is retried next launch. Old files are never modified.
//
// Group sends wait for the groups answer (groupSendsOpen): the daemon's
// set_project_group creates a group it does not have, and an import is
// refused by a daemon that already holds one — so a send that reached the
// daemon first would make it drop every member this import carries. A group
// change made before the answer is applied to the file at once and its send
// is held (deferGroupOp), then replayed in order when the answer arrives.

type importMarkerKinds struct {
	Groups bool `json:"groups"`
	Recent bool `json:"recent"`
	Notes  bool `json:"notes"`
}

type sharedImportMarker struct {
	Version int                          `json:"version"`
	Dests   map[string]importMarkerKinds `json:"dests"`
}

// sharedImportRespMsg is a shared_import_resp. An IPC response: its Update
// arm re-arms listenForMessages.
type sharedImportRespMsg struct {
	dest string
	id   string
	resp ipc.SharedImportRespPayload
}

// sharedImportTimeoutMsg is a local timer; it does not re-arm the listen.
type sharedImportTimeoutMsg struct{ id string }

var sharedImportTimeout = 8 * time.Second

// importReserveBytes is left out of the request budget for the groups, the
// recent list and the JSON around the notes.
const importReserveBytes = 64 << 10

// maxDeferredGroupOps bounds the sends held for one destination while its
// groups import is unanswered. The changes themselves are in the file.
const maxDeferredGroupOps = 64

// deferredGroupOp is one group send held until the groups answer.
type deferredGroupOp struct {
	msgType string
	payload any
	what    string
}

// SetSharedImportMarker turns the import on; a Model that never had this
// called (every test-built one) imports nothing — the SetRecentCWDs rule.
func (m *Model) SetSharedImportMarker(path string) { m.importMarkerPath = path }

func loadImportMarker(path string) sharedImportMarker {
	mk := sharedImportMarker{Version: 1, Dests: map[string]importMarkerKinds{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return mk
	}
	if err := json.Unmarshal(data, &mk); err != nil || mk.Dests == nil {
		log.Printf("shared import: marker unreadable, starting over: %v", err)
		return sharedImportMarker{Version: 1, Dests: map[string]importMarkerKinds{}}
	}
	return mk
}

func saveImportMarker(path string, mk sharedImportMarker) error {
	data, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// groupsImportAnswered reports whether dest's daemon has answered this
// client's groups import — in the marker from an earlier launch, or this
// session. From then on the daemon, not the file, holds dest's members.
func (m *Model) groupsImportAnswered(dest string) bool {
	return m.groupsImported[dest]
}

// groupSendsOpen reports whether a group send may go to dest now. With the
// import off (no marker path) nothing is imported, so nothing can race it.
func (m *Model) groupSendsOpen(dest string) bool {
	return m.importMarkerPath == "" || m.groupsImportAnswered(dest)
}

// deferGroupOp holds one group send for dest until its groups answer. A
// later set_project_group for the same project replaces the earlier one and
// moves to the end, so the replay keeps the order against group creates.
func (m *Model) deferGroupOp(dest, msgType string, payload any, what string) {
	if m.deferredGroupOps == nil {
		m.deferredGroupOps = map[string][]deferredGroupOp{}
	}
	ops := m.deferredGroupOps[dest]
	if set, ok := payload.(ipc.SetProjectGroupPayload); ok {
		kept := ops[:0]
		for _, op := range ops {
			if prev, ok := op.payload.(ipc.SetProjectGroupPayload); ok && prev.ProjectID == set.ProjectID {
				continue
			}
			kept = append(kept, op)
		}
		ops = kept
	}
	if len(ops) >= maxDeferredGroupOps {
		log.Printf("groups: %s for %q not sent — too many changes wait for the import answer; the file keeps it", what, dest)
		return
	}
	m.deferredGroupOps[dest] = append(ops, deferredGroupOp{msgType: msgType, payload: payload, what: what})
}

// paneIDsByDest is every connected destination's live pane ids, from
// m.projects — what decides whether a note file's pane id is ambiguous.
func (m *Model) paneIDsByDest() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range m.projects {
		if p == nil || p.Offline != nil {
			continue
		}
		set := out[p.Dest]
		if set == nil {
			set = map[string]bool{}
			out[p.Dest] = set
		}
		for _, tab := range p.tabs {
			if tab == nil {
				continue
			}
			if tab.Root != nil {
				for id := range tab.Root.PaneIDs() {
					set[id] = true
				}
			}
			if tab.overlayPane != nil {
				set[tab.overlayPane.ID] = true
			}
		}
	}
	return out
}

// maybeImport runs on every shared frame, after applyWorkspaceState (the
// pane ids) and before rebuildGroupsView (which reads groupsImportAnswered).
// The groups it sends are m.groups — the file's view — for this destination.
//
// The request is sent HERE, on the Update goroutine, rather than from a Cmd:
// a Cmd runs on its own goroutine with no order against anything else, and
// the import must be on the wire before any group send can follow it.
func (m *Model) maybeImport(msg WorkspaceStateMsg) tea.Cmd {
	if !msg.SharedData || m.importMarkerPath == "" || m.importAsked[msg.Dest] {
		return nil
	}
	if m.importAsked == nil {
		m.importAsked = map[string]bool{}
		m.pendingImports = map[string]string{}
		m.groupsImported = map[string]bool{}
	}
	m.importAsked[msg.Dest] = true
	mk := loadImportMarker(m.importMarkerPath)
	key := config.DestFileKey(msg.Dest)
	kinds := mk.Dests[key]
	if msg.Dest == "" && !kinds.Notes {
		// F-12: the local daemon adopts the files itself (restore, note_rev 1).
		kinds.Notes = true
		mk.Dests[key] = kinds
		if err := saveImportMarker(m.importMarkerPath, mk); err != nil {
			log.Printf("shared import: marker: %v", err)
		}
	}
	if kinds.Groups {
		m.groupsImported[msg.Dest] = true
	}
	var want []string
	if !kinds.Groups {
		want = append(want, ipc.ImportKindGroups)
	}
	if !kinds.Recent {
		want = append(want, ipc.ImportKindRecent)
	}
	if !kinds.Notes {
		want = append(want, ipc.ImportKindNotes)
	}
	if len(want) == 0 {
		return nil
	}
	dest := msg.Dest
	payload := ipc.SharedImportPayload{Kinds: want}
	if !kinds.Groups {
		// This destination's members; a group with no member anywhere goes to
		// the LOCAL daemon only, so the name lives on.
		for _, grp := range m.groups.Groups {
			var ids []string
			for _, mb := range grp.Members {
				if mb.Dest == dest {
					ids = append(ids, mb.ID)
				}
			}
			if len(ids) > 0 || (dest == "" && len(grp.Members) == 0) {
				payload.Groups = append(payload.Groups, ipc.SharedImportGroup{Name: grp.Name, ProjectIDs: ids})
			}
		}
	}
	if !kinds.Recent {
		payload.Recent = LoadRecentCWDs(config.RecentCWDsPath(dest))
	}
	if !kinds.Notes {
		// Mine is the frame's own pane list — the daemon's whole state, a
		// pane in no tab included; the others come from what this client
		// holds of every other connected destination.
		mine := make(map[string]bool, len(msg.Panes))
		for _, p := range msg.Panes {
			mine[p.ID] = true
		}
		others := map[string]bool{}
		for d, set := range m.paneIDsByDest() {
			if d == dest {
				continue
			}
			for id := range set {
				others[id] = true
			}
		}
		payload.Notes = collectImportNotes(config.NotesDir(), mine, others, ipc.MaxSharedImportBytes-importReserveBytes)
	}
	req, err := ipc.NewMessage(ipc.MsgSharedImport, payload)
	if err != nil {
		log.Printf("shared import: encode: %v", err)
		return nil
	}
	id := "imp-" + m.nextReqGen()
	req.ID = id
	m.pendingImports[id] = dest
	if err := m.sendForDestStrict(dest, req); err != nil {
		delete(m.pendingImports, id)
		log.Printf("shared import %q: not sent: %v; retried next launch", dest, err)
		return nil
	}
	return tea.Tick(sharedImportTimeout, func(time.Time) tea.Msg { return sharedImportTimeoutMsg{id: id} })
}

// collectImportNotes reads every notes/<paneID>.md whose id is in mine and
// not in others, skipping (and logging) ambiguous ids, files over the note
// cap, and anything past budget — those wait for the next launch. The budget
// counts each note as JSON-encoded, which is what the daemon measures.
func collectImportNotes(dir string, mine, others map[string]bool, budget int) []ipc.SharedImportNote {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ipc.SharedImportNote
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		if !mine[id] {
			continue
		}
		if others[id] {
			log.Printf("shared import: note %s exists on two destinations; left in place", id)
			continue
		}
		if _, err := persist.NotesFileName(id); err != nil {
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > ipc.MaxNoteBytes {
			log.Printf("shared import: note %s is %d bytes, over the cap; left in place", id, info.Size())
			continue
		}
		text, err := persist.LoadNotes(dir, id)
		if err != nil || text == "" {
			continue
		}
		if len(text) > ipc.MaxNoteBytes {
			log.Printf("shared import: note %s is %d bytes, over the cap; left in place", id, len(text))
			continue
		}
		note := ipc.SharedImportNote{PaneID: id, Text: text}
		enc, err := json.Marshal(note)
		if err != nil {
			continue
		}
		if len(enc) > budget {
			log.Printf("shared import: note %s deferred to the next launch (request budget)", id)
			continue
		}
		budget -= len(enc) + 1 // the comma between list items
		out = append(out, note)
	}
	return out
}

// applySharedImportResp records every kind the daemon answered in the marker
// and, once groups are answered, sends the group changes held back until now.
func (m *Model) applySharedImportResp(msg sharedImportRespMsg) tea.Cmd {
	dest, ok := m.pendingImports[msg.id]
	if !ok || dest != msg.dest {
		return nil
	}
	delete(m.pendingImports, msg.id)
	mk := loadImportMarker(m.importMarkerPath)
	key := config.DestFileKey(dest)
	kinds := mk.Dests[key]
	groupsNow := false
	for _, k := range msg.resp.Answered {
		switch k {
		case ipc.ImportKindGroups:
			kinds.Groups = true
			groupsNow = true
		case ipc.ImportKindRecent:
			kinds.Recent = true
		case ipc.ImportKindNotes:
			kinds.Notes = true
		}
	}
	mk.Dests[key] = kinds
	if err := saveImportMarker(m.importMarkerPath, mk); err != nil {
		log.Printf("shared import: marker: %v", err)
	}
	log.Printf("shared import %q: answered=%v groups=%v recent=%v notes=%d/%d",
		dest, msg.resp.Answered, msg.resp.GroupsApplied, msg.resp.RecentApplied, msg.resp.NotesApplied, msg.resp.NotesSkipped)
	if !groupsNow {
		return nil
	}
	m.groupsImported[dest] = true
	held := m.deferredGroupOps[dest]
	delete(m.deferredGroupOps, dest)
	var cmds []tea.Cmd
	for _, op := range held {
		cmds = append(cmds, m.sendSharedOp(dest, op.msgType, op.payload, op.what))
	}
	return tea.Batch(cmds...)
}

// applySharedImportTimeout only logs. The request stays pending: a late
// answer is still the daemon's answer, and it is what opens group sends.
func (m *Model) applySharedImportTimeout(msg sharedImportTimeoutMsg) {
	if dest, ok := m.pendingImports[msg.id]; ok {
		log.Printf("shared import %q: no answer yet; the marker is unchanged, retried next launch", dest)
	}
}
