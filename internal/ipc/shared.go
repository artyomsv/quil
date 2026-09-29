package ipc

// Shared data on the daemon (phase 3b, #237): project groups, recent folders
// and pane notes are held by each daemon and reach every client of it.
//
// Every request here is id-bearing. The group mutations are answered with
// OpRespPayload (answerOp) exactly like the project mutations; the note and
// import requests have their own responses because they carry data.

const (
	MsgSetProjectGroup  = "set_project_group"  // client → daemon (SetProjectGroupPayload), answered with project_op_resp
	MsgGroupOp          = "group_op"           // client → daemon (GroupOpPayload), answered with group_op_resp
	MsgGroupOpResp      = "group_op_resp"      // daemon → client (OpRespPayload; ID = the group name)
	MsgNoteGet          = "note_get"           // client → daemon (NoteGetPayload)
	MsgNoteResp         = "note_resp"          // daemon → client (NoteRespPayload)
	MsgNoteSet          = "note_set"           // client → daemon (NoteSetPayload)
	MsgNoteSetResp      = "note_set_resp"      // daemon → client (NoteSetRespPayload)
	MsgSharedImport     = "shared_import"      // client → daemon (SharedImportPayload)
	MsgSharedImportResp = "shared_import_resp" // daemon → client (SharedImportRespPayload)
)

// CapSharedData is listed in hello_resp and, for the TUI (which does not read
// hello_resp), stated on every workspace_state frame as shared_data: true.
const CapSharedData = "shared_data"

// Caps enforced daemon-side. The client mirrors them only to fail early.
const (
	MaxNoteBytes         = 256 << 10
	MaxGroupNameRunes    = 32
	MaxGroupsPerDaemon   = 64
	MaxRecentCWDs        = 5
	MaxSharedImportBytes = 8 << 20
)

// Group operations for GroupOpPayload.Op. There is no "move": display order
// is per client (spec D-1a).
const (
	GroupOpCreate = "create"
	GroupOpRename = "rename"
	GroupOpDelete = "delete"
)

// SetProjectGroupPayload files one project under a group name; "" ungroups.
type SetProjectGroupPayload struct {
	ProjectID string `json:"project_id"`
	Group     string `json:"group"`
}

// GroupOpPayload creates, renames or deletes a group name on this daemon.
type GroupOpPayload struct {
	Op      string `json:"op"`
	Name    string `json:"name"`
	NewName string `json:"new_name,omitempty"`
}

type NoteGetPayload struct {
	PaneID string `json:"pane_id"`
}

// NoteRespPayload answers note_get. Rev 0 and an empty Text mean no note.
type NoteRespPayload struct {
	PaneID string `json:"pane_id"`
	Text   string `json:"text"`
	Rev    uint64 `json:"rev"`
	Error  string `json:"error,omitempty"`
}

// NoteSetPayload saves a note that was loaded (or last saved) at BaseRev.
type NoteSetPayload struct {
	PaneID  string `json:"pane_id"`
	Text    string `json:"text"`
	BaseRev uint64 `json:"base_rev"`
}

// NoteSetRespPayload answers note_set. Conflict means BaseRev was stale and
// nothing was written; CurrentRev is what the daemon holds.
type NoteSetRespPayload struct {
	PaneID     string `json:"pane_id"`
	OK         bool   `json:"ok"`
	Rev        uint64 `json:"rev,omitempty"`
	Conflict   bool   `json:"conflict,omitempty"`
	CurrentRev uint64 `json:"current_rev,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Import kinds for SharedImportPayload.Kinds.
const (
	ImportKindGroups = "groups"
	ImportKindRecent = "recent"
	ImportKindNotes  = "notes"
)

type SharedImportGroup struct {
	Name       string   `json:"name"`
	ProjectIDs []string `json:"project_ids,omitempty"`
}

type SharedImportNote struct {
	PaneID string `json:"pane_id"`
	Text   string `json:"text"`
}

// SharedImportPayload hands a client's old files to the daemon, once. Kinds
// names which of the three lists this request carries; a kind not listed is
// neither applied nor answered, so its marker stays pending.
type SharedImportPayload struct {
	Kinds  []string            `json:"kinds"`
	Groups []SharedImportGroup `json:"groups,omitempty"`
	Recent []string            `json:"recent,omitempty"`
	Notes  []SharedImportNote  `json:"notes,omitempty"`
}

// SharedImportRespPayload says which kinds were handled (applied or skipped
// because the daemon already held data of that kind) — the client sets its
// marker for every kind in Answered.
type SharedImportRespPayload struct {
	Answered      []string `json:"answered"`
	GroupsApplied bool     `json:"groups_applied,omitempty"`
	RecentApplied bool     `json:"recent_applied,omitempty"`
	NotesApplied  int      `json:"notes_applied,omitempty"`
	NotesSkipped  int      `json:"notes_skipped,omitempty"`
	Error         string   `json:"error,omitempty"`
}
