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

// createPaneTimeoutMsg fires when a worktree create has not answered. tabID
// scopes it to the create that armed it, so a late tick cannot unwind a
// placeholder a LATER create is legitimately holding.
type createPaneTimeoutMsg struct{ tabID string }

// createPaneRespMsg carries the daemon's answer to a create that asked for a
// new worktree. Only such creates get one — an ordinary create is synchronous
// and its result arrives in the next workspace broadcast.
type createPaneRespMsg struct {
	Resp ipc.CreatePaneRespPayload
}

// createPaneTimeout bounds the wait for that answer.
//
// Deliberately LONGER than the daemon's worktreeAddTimeout: giving up first
// would prune a placeholder the daemon is about to fill, so the pane would
// arrive with nowhere to go. The two are tied together here rather than being
// two independent numbers that can drift apart silently.
// A var, not a const, so the test binary can shorten it — the same reason
// worktreeScanTimeout is one. A test that drives the real send executes the
// tick alongside it, and at the production value that is a two-and-a-half
// minute unit test.
var createPaneTimeout = worktreeAddTimeout + 30*time.Second

// worktreeAddTimeout mirrors the daemon constant of the same name. Duplicated
// rather than imported because internal/daemon is not an import this package
// takes — but the RELATIONSHIP is what matters and is asserted by
// TestCreatePaneTimeout_ExceedsTheDaemonAddTimeout.
const worktreeAddTimeout = 120 * time.Second

// applyCreatePaneResp settles a worktree-backed create.
//
// SUCCESS needs nothing here: the pane arrives in the next workspace broadcast
// and fills the placeholder exactly as an ordinary create does.
//
// FAILURE has to unwind by hand, and nothing else will. handleCreatePaneSplit
// splits the layout tree and arms pendingSplit BEFORE the send, and
// applyWorkspaceState only FILLS placeholders — it never retires one whose
// pane never arrives. Without this the tab keeps a dead placeholder leaf and
// the next pane created anywhere in that tab is swallowed by it.
//
// That window is a microsecond for an ordinary create. A worktree add stretches
// it to seconds, which is what makes the failure reachable at all.
func (m *Model) applyCreatePaneResp(p ipc.CreatePaneRespPayload) {
	tabID := m.worktreeCreateTab
	m.worktreeCreateTab = ""
	if p.Error == "" {
		return
	}
	if tab := m.tabByID(tabID); tab != nil && tab.Root != nil {
		tab.Root.PrunePlaceholders()
		tab.invalidateLeaves()
	}
	delete(m.pendingSplit, tabID)
	// git's own stderr, sanitized at render like every daemon-sourced string:
	// under --remote this text comes from a host the user may not control.
	m.setFlash("worktree not created: " + sanitizeRemoteText(p.Error))
}

// applyCreatePaneTimeout unwinds a create that never answered, so a wedged or
// restarted daemon cannot leave the tab holding a placeholder forever.
func (m *Model) applyCreatePaneTimeout(tabID string) {
	if m.worktreeCreateTab != tabID {
		return // already settled by a response
	}
	m.applyCreatePaneResp(ipc.CreatePaneRespPayload{
		Error: "timed out waiting for the worktree to be created",
	})
}
