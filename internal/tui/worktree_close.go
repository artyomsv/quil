package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// confirmWorktree is one worktree the close dialog is offering to delete.
//
// path is the DAEMON's directory and is never rendered raw as an identifier —
// it is the key the status response is matched on. label is what the user
// reads: the branch, which is what they named when they created the worktree.
//
// loaded distinguishes "answered" from "still checking", and err from a zero
// count: a change count nobody obtained must not render as clean, because
// clean is the one answer that invites the toggle.
type confirmWorktree struct {
	path    string
	label   string
	changes int
	err     string
	loaded  bool
}

// collectConfirmWorktrees reports the worktrees a close would delete.
//
// WorktreeOwned is the only gate, mirroring ownedWorktreePaths daemon-side —
// and the mirroring is deliberate rather than redundant. The daemon's copy is
// the one that decides what is deleted (the wire carries a bool, not a path);
// this one decides what is OFFERED, and offering a checkout Quil did not create
// would be a claim about someone else's directory even if the daemon then
// refused it.
//
// Deduplicated by path: two panes split inside one worktree describe one
// directory, and listing it twice would tell the user a tab holds two.
func collectConfirmWorktrees(panes []*PaneModel) []confirmWorktree {
	var out []confirmWorktree
	seen := map[string]bool{}
	for _, p := range panes {
		if p == nil || !p.WorktreeOwned || p.CWD == "" || seen[p.CWD] {
			continue
		}
		seen[p.CWD] = true
		out = append(out, confirmWorktree{path: p.CWD, label: confirmWorktreeLabel(p)})
	}
	return out
}

// confirmWorktreeLabel names a worktree for the close dialog: its branch, else the worktree
// name git gave the directory, else the path itself.
//
// The branch first because it is what the user typed when the worktree was
// created, and what they will look for in `git branch` afterwards — the row's
// job is to let them recognise which piece of work they are about to delete the
// checkout of.
func confirmWorktreeLabel(p *PaneModel) string {
	if p.GitBranch != "" {
		return p.GitBranch
	}
	if p.GitWorktreeName != "" {
		return p.GitWorktreeName
	}
	return p.CWD
}

// worktreeStatusMsg carries one status response. Gen echoes the requesting
// message's ID — see Model.confirmWorktreeGen.
type worktreeStatusMsg struct {
	Resp ipc.WorktreeStatusRespPayload
	Gen  string
}

// worktreeStatusTimeoutMsg fires when a status request went unanswered.
type worktreeStatusTimeoutMsg struct{ gen string }

// worktreeStatusTimeout bounds one status round trip from the client side.
//
// Well inside the daemon's own 10 s bound, unlike worktreeScanTimeout beside
// it, and for a reason specific to this dialog: the answer is ADVISORY. The
// toggle works whether or not it arrives, so giving up early costs the user a
// number and never a decision — while a dialog that sits on "checking…" for
// ten seconds looks wedged in front of somebody who is trying to close a pane.
// A var so the test binary can shorten it.
var worktreeStatusTimeout = 6 * time.Second

// requestWorktreeStatus asks the daemon how much uncommitted work the dialog's
// worktrees hold.
//
// One request for every worktree in the dialog, so a tab with several is one
// round trip. Returns nil when there is nothing to ask about — an ordinary
// close must cost no IPC at all.
func (m *Model) requestWorktreeStatus(dest string) tea.Cmd {
	if len(m.confirmWorktrees) == 0 {
		m.confirmWorktreeGen = ""
		return nil
	}
	paths := make([]string, 0, len(m.confirmWorktrees))
	for _, w := range m.confirmWorktrees {
		paths = append(paths, w.path)
	}
	gen := m.nextReqGen()
	m.confirmWorktreeGen = gen
	client := m.client
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgWorktreeStatusReq, ipc.WorktreeStatusReqPayload{Paths: paths})
			if err != nil {
				log.Printf("worktree status: encode: %v", err)
				return nil
			}
			// respondTo echoes ID verbatim, which is what lets
			// applyWorktreeStatus tell one dialog's request from the next's.
			msg.ID = gen
			if client != nil {
				// stampDest + Send is sendForDest inlined, because a Cmd runs on
				// its own goroutine and must not reach back into the Model: dest
				// was resolved by the caller on the Update goroutine, which is
				// the only place that may read m.projects.
				stampDest(msg, dest)
				if err := client.Send(msg); err != nil {
					log.Printf("worktree status: send: %v", err)
				}
			}
			return nil
		},
		tea.Tick(worktreeStatusTimeout, func(time.Time) tea.Msg {
			return worktreeStatusTimeoutMsg{gen: gen}
		}),
	)
}

// applyWorktreeStatus lands a response on the rows it describes.
//
// Matched on the generation, not on the paths: the user can close one pane and
// open the confirm for another within the round trip, and two dialogs asking
// about the same worktree are wire-indistinguishable otherwise. A late
// response then paints a row the current dialog never asked about.
func (m *Model) applyWorktreeStatus(msg worktreeStatusMsg) {
	if m.confirmWorktreeGen == "" || msg.Gen != m.confirmWorktreeGen {
		return
	}
	m.confirmWorktreeGen = ""
	byPath := map[string]ipc.WorktreeStatus{}
	for _, st := range msg.Resp.Statuses {
		byPath[st.Path] = st
	}
	for i := range m.confirmWorktrees {
		row := &m.confirmWorktrees[i]
		row.loaded = true
		st, ok := byPath[row.path]
		switch {
		case msg.Resp.Error != "":
			// A whole-request refusal describes every row, and none of them was
			// read — so none may claim to be clean.
			row.err = msg.Resp.Error
		case !ok:
			row.err = "no answer for this worktree"
		case st.Error != "":
			row.err = st.Error
		default:
			row.changes = st.Changes
		}
	}
}

// applyWorktreeStatusTimeout gives up on a request.
//
// The rows become LOADED carrying a reason rather than staying on "checking…":
// the dialog is usable either way, and a spinner that never resolves is the
// state the bound exists to remove. A reason, never a zero count — "could not
// check" and "clean" are different answers and only one of them invites the
// toggle.
func (m *Model) applyWorktreeStatusTimeout(msg worktreeStatusTimeoutMsg) {
	if m.confirmWorktreeGen == "" || msg.gen != m.confirmWorktreeGen {
		return
	}
	m.confirmWorktreeGen = ""
	for i := range m.confirmWorktrees {
		m.confirmWorktrees[i].loaded = true
		m.confirmWorktrees[i].err = "could not check for uncommitted work"
	}
}

// resetConfirmWorktrees clears every field the worktree row owns.
//
// Called on EVERY route out of the confirm dialog. These fields outlive the
// dialog that set them, and an armed toggle inherited by the next close is a
// deletion the user was never offered — the same class of bug the project
// form's plan-arming had, and the reason that one re-derives its plan on open.
func (m *Model) resetConfirmWorktrees() {
	m.confirmWorktrees = nil
	m.confirmRemoveWorktree = false
	m.confirmWorktreeGen = ""
}
