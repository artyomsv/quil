package tui

import (
	"encoding/json"
	"fmt"
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

// sharedImportErrMsg is an error reply to a shared_import (the daemon
// refused the request itself, e.g. a bad payload). An IPC response: its
// Update arm re-arms listenForMessages.
type sharedImportErrMsg struct {
	dest string
	id   string
	text string
}

// sharedImportTimeoutMsg is a local timer; it does not re-arm the listen.
type sharedImportTimeoutMsg struct{ id string }

// pendingImport is one shared_import awaiting its answer.
type pendingImport struct {
	dest string
	// notesDeferred: the request budget left notes out, so a "notes" answer
	// does not finish the kind — the rest go with the next launch's import.
	notesDeferred bool
}

// maxImportErrors is how many error replies one destination may give this
// session before its import waits for the next launch instead of re-sending.
const maxImportErrors = 3

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

// saveImportMarker writes the marker through a uniquely named temp file and a
// rename, the saveProjectGroups pattern: two clients sharing a QUIL_HOME never
// write the same temp path.
func saveImportMarker(path string, mk sharedImportMarker) error {
	data, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return fmt.Errorf("encode import marker: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			// Best effort: the error being returned is the one worth reporting.
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmpPath, err)
	}
	committed = true
	return nil
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
		log.Printf("groups: %s for %q DROPPED — %d changes already wait for the import answer; the daemon's next frame will undo it", what, dest, len(ops))
		return
	}
	m.deferredGroupOps[dest] = append(ops, deferredGroupOp{msgType: msgType, payload: payload, what: what})
	if op, ok := payload.(ipc.GroupOpPayload); ok {
		m.noteHeldGroupName(dest, op)
	}
}

// noteHeldGroupName keeps importNames[dest] — the names dest's daemon will
// list once the import and the held ops land — in step with a held group op,
// so a later rename or delete of such a name is held for dest too.
func (m *Model) noteHeldGroupName(dest string, op ipc.GroupOpPayload) {
	if m.importNames == nil {
		m.importNames = map[string][]string{}
	}
	names := m.importNames[dest]
	drop := func(name string) {
		kept := names[:0]
		for _, n := range names {
			if !strings.EqualFold(n, name) {
				kept = append(kept, n)
			}
		}
		names = kept
	}
	switch op.Op {
	case ipc.GroupOpCreate:
		if !containsFold(names, op.Name) {
			names = append(names, op.Name)
		}
	case ipc.GroupOpRename:
		drop(op.Name)
		names = append(names, op.NewName)
	case ipc.GroupOpDelete:
		drop(op.Name)
	}
	m.importNames[dest] = names
}

// destsHoldingGroupName is every shared, connected destination whose groups
// import is unanswered and whose daemon will list name once it lands — the
// held half of a rename or delete fan-out (sendGroupOpEverywhere).
func (m *Model) destsHoldingGroupName(name string) []string {
	var out []string
	for _, d := range m.sharedDestsInOrder() {
		if !m.groupSendsOpen(d) && m.destConnected(d) && containsFold(m.importNames[d], name) {
			out = append(out, d)
		}
	}
	return out
}

// forgetImportFor drops dest's in-flight import: its answer cannot arrive on
// a new connection, so the next shared frame from dest sends it again. The
// daemon side is idempotent — a refusal is an answer, which opens group sends
// and replays the held ops. The held ops themselves are kept.
func (m *Model) forgetImportFor(dest string) {
	for id, p := range m.pendingImports {
		if p.dest == dest {
			delete(m.pendingImports, id)
		}
	}
	delete(m.importAsked, dest)
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
		m.pendingImports = map[string]pendingImport{}
		m.groupsImported = map[string]bool{}
		m.importNames = map[string][]string{}
		m.importErrors = map[string]int{}
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
	notesDeferred := false
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
		payload.Notes, notesDeferred = collectImportNotes(config.NotesDir(), mine, others, ipc.MaxSharedImportBytes-importReserveBytes)
	}
	req, err := ipc.NewMessage(ipc.MsgSharedImport, payload)
	if err != nil {
		log.Printf("shared import: encode: %v", err)
		return nil
	}
	id := "imp-" + m.nextReqGen()
	req.ID = id
	m.pendingImports[id] = pendingImport{dest: dest, notesDeferred: notesDeferred}
	if err := m.sendForDestStrict(dest, req); err != nil {
		delete(m.pendingImports, id)
		delete(m.importAsked, dest) // the next frame from dest tries again
		log.Printf("shared import %q: not sent: %v; retried on its next frame", dest, err)
		return nil
	}
	if !kinds.Groups {
		// The file view already holds every held change, so the payload's
		// names are what this daemon will list once the import lands.
		names := make([]string, 0, len(payload.Groups))
		for _, g := range payload.Groups {
			names = append(names, g.Name)
		}
		m.importNames[dest] = names
	}
	return tea.Tick(sharedImportTimeout, func(time.Time) tea.Msg { return sharedImportTimeoutMsg{id: id} })
}

// collectImportNotes reads every notes/<paneID>.md whose id is in mine and
// not in others, skipping (and logging) ambiguous ids, files over the note
// cap, and anything past budget — those wait for the next launch, and
// deferred reports that some did. The budget counts each note as
// JSON-encoded, which is what the daemon measures.
func collectImportNotes(dir string, mine, others map[string]bool, budget int) (out []ipc.SharedImportNote, deferred bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
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
			deferred = true
			continue
		}
		budget -= len(enc) + 1 // the comma between list items
		out = append(out, note)
	}
	return out, deferred
}

// applySharedImportResp records every kind the daemon answered in the marker
// and, once groups are answered, sends the group changes held back until now.
func (m *Model) applySharedImportResp(msg sharedImportRespMsg) tea.Cmd {
	p, ok := m.pendingImports[msg.id]
	if !ok || p.dest != msg.dest {
		return nil
	}
	dest := p.dest
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
			// Notes the budget left out are still pending: the next launch
			// sends them (the daemon skips a pane that already has a note).
			kinds.Notes = !p.notesDeferred
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
	delete(m.importNames, dest)
	var cmds []tea.Cmd
	for _, op := range held {
		cmds = append(cmds, m.sendSharedOp(dest, op.msgType, op.payload, op.what))
	}
	return tea.Batch(cmds...)
}

// applySharedImportTimeout only logs. The request stays pending: a late
// answer is still the daemon's answer, and it is what opens group sends. A
// lost link drops it instead (forgetImportFor), and the reattach re-sends.
func (m *Model) applySharedImportTimeout(msg sharedImportTimeoutMsg) {
	if p, ok := m.pendingImports[msg.id]; ok {
		log.Printf("shared import %q: no answer yet; still waiting (a reconnect sends it again)", p.dest)
	}
}

// applySharedImportErr handles an error reply to this client's import: the
// daemon refused the request as a whole, so nothing is answered and the
// marker is untouched. The next shared frame from dest sends it again, until
// dest has given maxImportErrors error replies this session; then it waits
// for the next launch, and group sends to dest stay held until then.
func (m *Model) applySharedImportErr(msg sharedImportErrMsg) {
	p, ok := m.pendingImports[msg.id]
	if !ok || p.dest != msg.dest {
		return
	}
	delete(m.pendingImports, msg.id)
	m.importErrors[p.dest]++
	if n := m.importErrors[p.dest]; n < maxImportErrors {
		delete(m.importAsked, p.dest)
		log.Printf("shared import %q refused (%d/%d): %s; sent again on its next frame", p.dest, n, maxImportErrors, msg.text)
		return
	}
	log.Printf("shared import %q refused (%d/%d): %s; retried next launch", p.dest, m.importErrors[p.dest], maxImportErrors, msg.text)
}
