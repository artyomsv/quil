package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// worktreeListMsg carries one worktree-listing response. Gen echoes the
// requesting message's ID — see worktreeState.gen.
type worktreeListMsg struct {
	Resp ipc.WorktreeListRespPayload
	Gen  string
}

// worktreeTimeoutMsg fires when a listing request went unanswered.
type worktreeTimeoutMsg struct{ path, gen string }

// worktreeScanTimeout bounds one listing round trip from the client side.
// Matches gitScanTimeout for the same reason: this request can cross ssh,
// where a first round trip after an idle period pays a handshake. Still inside
// the daemon's own 10 s bound, so a slow-but-working scan reports rather than
// being pre-empted here. A var so the test binary can shorten it.
var worktreeScanTimeout = 8 * time.Second

// worktreeState tracks the worktree listing for one directory.
//
// pending is an explicit flag rather than a derived one because "" is a VALID
// path (the daemon's default directory), so the zero value cannot double as
// the idle sentinel — the same constraint browseState documents.
//
// loaded distinguishes "answered" from "never asked": a directory that is not
// a repository produces repo=false with no error, and the field must render
// that differently from a scan still in flight.
type worktreeState struct {
	path    string // echoed back by the daemon; identifies WHAT was asked
	gen     string // identifies WHICH request; see requestWorktrees
	pending bool
	loaded  bool

	repo         bool
	root         string
	worktreeRoot string
	list         []ipc.WorktreeInfo
	err          string
}

// requestWorktrees asks the daemon which worktrees the repository containing
// path has. Used in local mode too, deliberately: the answer is identical when
// the daemon is local, and a path exercised only by remote sessions is one
// that rots.
func (m *Model) requestWorktrees(path string) tea.Cmd {
	gen := m.nextReqGen()
	m.worktrees = worktreeState{path: path, gen: gen, pending: true}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgWorktreeListReq, ipc.WorktreeListReqPayload{Path: path})
			if err != nil {
				log.Printf("worktree list: encode: %v", err)
				return nil
			}
			// respondTo echoes ID verbatim, which is what lets
			// applyWorktreeList tell two requests for the same path apart.
			msg.ID = gen
			m.client.Send(msg)
			return nil
		},
		worktreeTimeoutCmd(path, gen),
	)
}

func worktreeTimeoutCmd(path, gen string) tea.Cmd {
	return tea.Tick(worktreeScanTimeout, func(time.Time) tea.Msg {
		return worktreeTimeoutMsg{path: path, gen: gen}
	})
}

// applyWorktreeList lands a response, dropping any that does not match the
// request currently in flight on BOTH keys.
func (m *Model) applyWorktreeList(msg worktreeListMsg) {
	if !m.worktrees.pending || msg.Gen != m.worktrees.gen || msg.Resp.Path != m.worktrees.path {
		return
	}
	m.worktrees.pending = false
	m.worktrees.loaded = true
	m.worktrees.repo = msg.Resp.Repo
	m.worktrees.root = msg.Resp.Root
	m.worktrees.worktreeRoot = msg.Resp.WorktreeRoot
	m.worktrees.list = msg.Resp.Worktrees
	m.worktrees.err = msg.Resp.Error
}

// applyWorktreeTimeout gives up on a request. Gated on pending AND both keys:
// a previous request's late tick would otherwise wipe the listing the current
// one just delivered.
func (m *Model) applyWorktreeTimeout(msg worktreeTimeoutMsg) {
	if !m.worktrees.pending || msg.gen != m.worktrees.gen || msg.path != m.worktrees.path {
		return
	}
	m.worktrees.pending = false
	m.worktrees.loaded = true
	// A reason, not an empty list: "the scan never answered" and "this
	// repository has one worktree" must not render identically.
	m.worktrees.err = "worktree scan timed out"
}
