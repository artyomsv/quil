package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/gitworktree"
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
	// branches is every LOCAL branch of the repository, short. It exists
	// because list cannot answer "is this name taken": that one reports only
	// branches with a checkout, and the ordinary collision is with a branch
	// whose worktree was removed — invisible there, and the failure this was
	// added for.
	//
	// branchesTruncated says the daemon clipped the list. It is deliberately NOT
	// acted on: the check only ever refuses on a POSITIVE match, which stays a
	// true positive however short the list is, and nothing here claims a name is
	// available. Carried so a future affirmation ("✓ free") cannot be written
	// against a list that is not evidence of absence.
	branches          []string
	branchesTruncated bool
	err               string
}

// branchTaken reports whether the repository already has a branch by this name,
// i.e. whether `git worktree add -b` will refuse it.
//
// EXACT comparison. A listing is git's spelling and the field is the user's;
// folding case would refuse `Feat/X` on a repository holding `feat/x`, which git
// accepts wherever its ref store is case-sensitive — and refusing a name the
// user may legitimately create is the worse direction, because the message would
// simply be wrong. On a case-insensitive ref store the collision falls through
// to the daemon's own error, which the pane now shows.
//
// False for an empty list, which covers a directory that is not a repository, a
// listing that has not answered yet, a branch listing that failed, and a daemon
// too old to send one. Absence from a list nobody obtained is not evidence.
func (s worktreeState) branchTaken(name string) bool {
	for _, b := range s.branches {
		if b == name {
			return true
		}
	}
	return false
}

// validateNewBranch returns the message to show beside the name field, or "" when
// the name can be used.
//
// ONE function for BOTH validation sites — the name field's Enter and
// submitSetupDialog — because they must agree. They are reached by different
// routes (Tab is handled above the field dispatch, so tabbing away and pressing
// Continue never runs the field's Enter), and a check present in one and not the
// other is a name refused in the dialog and accepted by the button beside it.
//
// Syntax FIRST, so a malformed name is described as malformed rather than as
// absent from the branch list.
func (s worktreeState) validateNewBranch(name string) string {
	if err := gitworktree.ValidateBranch(name); err != nil {
		return err.Error()
	}
	if s.branchTaken(name) {
		// git's own wording, so a user who then hits the daemon's error on a
		// case-insensitive ref store reads the same sentence twice rather than
		// two descriptions of one fact.
		return "branch " + name + " already exists"
	}
	return ""
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
	m.worktrees.branches = msg.Resp.Branches
	m.worktrees.branchesTruncated = msg.Resp.BranchesTruncated
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
	// Dest is the destination the response arrived FROM (Message.Origin,
	// stamped by the router). Checked against the tab it names, so one daemon
	// cannot act on another daemon's tab.
	Dest string
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
// FAILURE has to unwind by hand. applyWorkspaceState DOES prune unfilled
// placeholders on every broadcast — but it is deliberately suppressed for a
// tab with a create in flight (see the exemption there), precisely because a
// worktree add holds its placeholder for SECONDS rather than the microseconds
// an ordinary create takes, and pruning mid-flight detaches the node that
// pendingSplit still points at. So while the exemption holds, this handler and
// the timeout beside it are the ONLY things that can retire the placeholder,
// and pendingSplit is never cleaned up by the broadcast path at all.
//
// Without this the tab keeps a dead placeholder leaf and the next pane created
// anywhere in that tab is swallowed by it.
func (m *Model) applyCreatePaneResp(p ipc.CreatePaneRespPayload, dest string) {
	// Keyed on what the DAEMON echoed, never on client-side "the last create
	// I started". protocol.go calls the echoed spec the client's staleness
	// key for exactly this reason: two creates can be in flight and their
	// responses can arrive in either order, so a scalar cursor unwinds the
	// wrong tab — or, worse, the wrong tab's LIVE placeholder.
	//
	// A nil spec means this is somebody else's create_pane_resp (the MCP
	// bridge uses the same message with no worktree), which owns no
	// placeholder of ours.
	if p.Worktree == nil {
		return
	}
	tabID := p.TabID
	if m.worktreeCreates[tabID] == "" {
		// A NEW-TAB create arms no tab-keyed bookkeeping — it detaches nothing
		// and reserves no leaf, so there is nothing here to unwind (see
		// handleCreatePaneSplit's new-tab branch). It cannot: the tab id does not
		// exist when the request leaves. But the daemon still answers, and a
		// failed add on that path is the user's ONLY notice — otherwise they ask
		// for an agent on a fresh branch, silently get a shell in the project
		// root, and the reason lives in quild.log alone.
		//
		// Matched on the echoed BRANCH, which is the key such a create does own,
		// and consumed on use. A blanket flash here would break the neighbouring
		// guarantee that a duplicate, late or foreign answer is inert
		// (TestCreatePaneResp_UnknownTabIsInert) — an MCP bridge's own worktree
		// create would raise an error the user never caused.
		if p.Error != "" && p.Worktree != nil && m.newTabWorktrees[p.Worktree.Branch] {
			delete(m.newTabWorktrees, p.Worktree.Branch)
			m.setFlash("worktree not created: " +
				truncateCells(sanitizeRemoteText(p.Error), createErrFlashCap))
		}
		return // not ours, or already settled
	}
	// The response must describe a tab on the daemon it came FROM. tabByID
	// walks every project on every destination, so without this a compromised
	// remote daemon could name a LOCAL tab — one this client really did arm —
	// and prune its live placeholder. The workspace_state arm already
	// dest-filters for the same reason; this handler mutates the layout tree,
	// so it needs it more.
	//
	// A tab that no longer exists is not a rejection: it was ours (the
	// worktreeCreates check above proves it), it was closed while the add ran,
	// and there is nothing left to prune — only the map entries to release.
	tab := m.tabByID(tabID)
	if tab != nil && tab.Dest != dest {
		return
	}
	// SUCCESS retires nothing — not even the exemption. The pane landing is
	// what does that (see applyWorkspaceState), so a daemon that answers "ok"
	// without creating a pane cannot get the placeholder pruned out from
	// under the create. The timeout is the backstop if no pane ever arrives.
	if p.Error == "" {
		// The one thing success DOES retire: a replaced pane held back in case
		// the add failed. The daemon has swapped it out, so the model describes
		// a pane that no longer exists — keeping it would leak its emulator,
		// and restoring it later would paint a dead pane.
		if old := m.worktreeReplaced[tabID]; old != nil {
			old.Dispose()
			delete(m.worktreeReplaced, tabID)
		}
		return
	}
	delete(m.worktreeCreates, tabID)
	// Retired in the same step as the map entry it mirrors. rebuildTabs
	// recomputes it from that map on every broadcast, so this only covers the
	// window before the next one — but an ordinary split started inside that
	// window would otherwise render "Creating worktree <the branch that just
	// failed>" over a placeholder that has no worktree at all.
	if tab != nil {
		tab.CreatingBranch = ""
	}
	// A failed REPLACE normally puts the pane back rather than pruning its
	// leaf: the worktree is created BEFORE the swap, so an add git refused
	// leaves the pane alive on both sides, and pruning would cost the user a
	// live pane over a branch name.
	//
	// Swapped is the exception, and it cannot be inferred from Error. The swap
	// happens before the new pane's PTY spawns, so a spawn failure reports an
	// error with the old pane ALREADY DESTROYED daemon-side. Restoring there
	// would put a pane the daemon no longer has back into the layout, and every
	// keystroke aimed at it would be dropped until the next broadcast pruned
	// the leaf. The daemon says which happened; this does not guess.
	m.settleReplacedPane(tabID, tab, !p.Swapped)
	if tab != nil && tab.Root != nil {
		tab.Root.PrunePlaceholders()
		tab.invalidateLeaves()
	}
	delete(m.pendingSplit, tabID)
	// git's own stderr, bounded then sanitized. Bounding is separate from
	// sanitizing and both are needed: sanitizeRemoteText removes escapes
	// without shortening anything, and the status bar drops its whole right
	// half rather than wrapping when a flash outgrows it.
	m.setFlash("worktree not created: " + truncateCells(sanitizeRemoteText(p.Error), createErrFlashCap))
}

// createErrFlashCap bounds git's stderr in the status-bar flash. A remote
// daemon chooses this text, and the same "sanitising is not bounding" rule the
// project form documents applies here.
const createErrFlashCap = 160

// settleReplacedPane retires the pane a worktree REPLACE detached, putting it
// back when restore is true and disposing it otherwise.
//
// One function because the three settling paths got this subtly different when
// they were written out separately: the timeout mutated the leaf BEFORE its
// `tab.Root != nil` guard, so a nil root left the leaf changed and the leaves
// cache stale. Restoring and invalidating belong in the same place, always in
// the same order.
//
// A no-op when nothing is held, so callers need no guard of their own.
func (m *Model) settleReplacedPane(tabID string, tab *TabModel, restore bool) {
	old := m.worktreeReplaced[tabID]
	if old == nil {
		return
	}
	delete(m.worktreeReplaced, tabID)
	// Restorable only if the leaf reserved for it is still there and still
	// empty. A tab closed mid-flight, or a leaf already refilled, leaves
	// nowhere to put it — and a model with no home is a leaked emulator.
	if restore && tab != nil && tab.Root != nil {
		if leaf := m.pendingSplit[tabID]; leaf != nil && leaf.Pane == nil {
			leaf.fill(old)
			tab.invalidateLeaves()
			return
		}
	}
	old.Dispose()
}

// applyCreatePaneTimeout unwinds a create that never answered, so a wedged or
// restarted daemon cannot leave the tab holding a placeholder forever.
func (m *Model) applyCreatePaneTimeout(tabID string) {
	if m.worktreeCreates[tabID] == "" {
		return // already settled by a response
	}
	delete(m.worktreeCreates, tabID)
	tab := m.tabByID(tabID)
	// Retired with the map entry, for the reason applyCreatePaneResp gives.
	if tab != nil {
		tab.CreatingBranch = ""
	}
	// Restored, like the failure path: nothing proved the swap happened, the
	// pane we detached is still ours, and putting it back is recoverable where
	// losing it is not. A create the daemon CONFIRMED already disposed and
	// cleared this entry — on the broadcast that filled the leaf — so a late
	// timeout cannot resurrect a replaced pane.
	m.settleReplacedPane(tabID, tab, true)
	if tab != nil && tab.Root != nil {
		tab.Root.PrunePlaceholders()
		tab.invalidateLeaves()
	}
	delete(m.pendingSplit, tabID)
	m.setFlash("worktree not created: timed out waiting for the worktree to be created")
}
