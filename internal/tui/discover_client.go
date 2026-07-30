package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// gitReposMsg carries one git-discovery response. Gen echoes the requesting
// message's ID — see repoScanState.gen for why a response needs one.
type gitReposMsg struct {
	Resp ipc.GitReposRespPayload
	Gen  string
}

// gitScanTimeoutMsg fires when a discovery request went unanswered.
type gitScanTimeoutMsg struct{ cwd, gen string }

// gitScanTimeout bounds one git-discovery round trip from the client side.
//
// Longer than the session picker's 3 s, which is tuned for a local socket. This
// request can cross ssh, where a first round trip after an idle period pays a
// TCP handshake and, on Windows, a full authentication — OpenSSH there has no
// ControlMaster to amortise it. Still well inside the daemon's own 10 s scan
// bound, so a slow-but-working scan reports its result rather than being
// pre-empted here.
//
// A var, not a const, purely so the test binary can shorten it — same
// reasoning and same TestMain override as browseTimeout in browse_client.go.
// Both the Alt+G overlay and the setup dialog's pick list route through
// requestGitRepos, so a test exercising either now pays this timer.
var gitScanTimeout = 8 * time.Second

// repoScanPurpose tells applyGitRepos which caller is waiting on a discovery
// response: the Alt+G overlay, or the setup dialog's git pick list. The two
// ask the same question of the daemon and are told apart only by this field —
// inferring the asker from which dialog happens to be open would break the
// moment a response arrives after the asker has moved on (Esc, plugin change,
// tab switch all leave no dialog-state trace of what was asked).
//
// repoScanOverlay is the zero value deliberately: it is the original,
// longer-lived consumer, so every repoScanState built before the pick list
// existed still means what it always meant.
type repoScanPurpose int

const (
	repoScanOverlay  repoScanPurpose = iota // Alt+G — resolveLazygitOverlay
	repoScanPickList                        // setup dialog's git pick list
)

// repoScanState tracks an in-flight git-discovery request.
//
// The zero value means "nothing in flight", matching the ctxMenu/palette
// convention in this package: no separate open bool that could drift.
type repoScanState struct {
	cwd     string // echoed back by the daemon; identifies WHAT was asked
	tabID   string // the tab that asked (repoScanOverlay only), resolved again on arrival
	purpose repoScanPurpose
	// gen identifies WHICH request this is, on top of cwd identifying what it
	// asked about. cwd alone is not enough: the overlay and the pick list can
	// both be asked about the SAME directory (e.g. Alt+G on a pane, then
	// Ctrl+N on a discover="git" plugin for that same pane, before Alt+G's
	// answer lands), and a purely content-keyed match cannot tell those two
	// requests' responses apart — a response could still be routed to the
	// PURPOSE that happened to overwrite this slot last, not the request that
	// actually produced it. gen is minted fresh per request (nextReqGen) and
	// carried both in the wire message's ID (echoed back verbatim by
	// respondTo, same as CWD) and in the local timeout closure, so both the
	// daemon's answer and this request's own timeout tick can be told apart
	// from a different request that happens to share the same cwd. Mirrors
	// Model.clientGen, which solves the identical problem for the reconnect
	// dial.
	gen string
}

// requestGitRepos asks the daemon which repositories are near cwd.
//
// The daemon is the only side that can answer honestly. gitdiscover run here
// stats THIS machine's filesystem, so against a remote host it reported "no git
// repo here" for a path that is a repository on the machine actually holding it
// — with nothing in the message hinting that the wrong disk had been consulted.
//
// Used in local mode too, deliberately. The answer is identical when the daemon
// is local, and a path exercised only by remote sessions is one that rots.
func (m *Model) requestGitRepos(cwd, tabID string, purpose repoScanPurpose) tea.Cmd {
	gen := m.nextReqGen()
	m.repoScan = repoScanState{cwd: cwd, tabID: tabID, purpose: purpose, gen: gen}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: cwd})
			if err != nil {
				log.Printf("git discovery: encode: %v", err)
				return nil
			}
			// The daemon's respondTo echoes ID back on the response verbatim
			// (same mechanism MCP request-response correlation uses) — this is
			// what lets applyGitRepos tell two requests for the same cwd apart.
			msg.ID = gen
			m.client.Send(msg)
			return nil
		},
		gitScanTimeoutCmd(cwd, gen),
	)
}

func gitScanTimeoutCmd(cwd, gen string) tea.Cmd {
	return tea.Tick(gitScanTimeout, func(time.Time) tea.Msg {
		return gitScanTimeoutMsg{cwd: cwd, gen: gen}
	})
}

// applyGitRepos resumes whichever caller is waiting on a git-discovery
// response — the Alt+G overlay, or the setup dialog's pick list — and routes
// to that caller's own reaction. Which one asked is read from
// m.repoScan.purpose, captured before the scan state is cleared.
//
// Responses for a directory the user has since left are dropped: either
// caller can issue a second request before the first answer lands, and acting
// on a superseded one would apply repositories that belong to a directory the
// user is no longer looking at. Matched on the echoed CWD AND gen — see
// repoScanState.gen for why CWD alone cannot tell two overlapping requests
// for the same directory apart.
func (m *Model) applyGitRepos(resp ipc.GitReposRespPayload, gen string) tea.Cmd {
	if resp.CWD != m.repoScan.cwd || gen != m.repoScan.gen {
		return nil
	}
	purpose := m.repoScan.purpose
	tabID := m.repoScan.tabID
	m.repoScan = repoScanState{}

	// A failure is NOT "no repository here". Reporting a timed-out or rejected
	// scan as an absent repo is a wrong answer stated confidently, which is the
	// whole failure mode this phase exists to remove. Logged once here — both
	// purposes want the log line, only the user-facing reaction differs.
	if resp.Error != "" {
		log.Printf("git discovery: %s", resp.Error)
		if purpose == repoScanPickList {
			return m.applyGitReposPickListError()
		}
		m.setFlash("repo scan failed")
		return m.flashCmd()
	}

	if purpose == repoScanPickList {
		return m.applyGitReposPickList(resp.Repos)
	}

	// Resolved again rather than captured: the request is asynchronous and the
	// user may have switched tabs while it was in flight. Acting on the tab that
	// asked keeps the overlay with its own pane; if that tab is gone, so is the
	// intent.
	tab := m.tabByID(tabID)
	if tab == nil {
		return nil
	}
	return m.resolveLazygitOverlay(tab, resp.Repos)
}

// tabByID finds a tab by id, or nil. Alt+G resolves its target twice — once to
// ask, once to act — because a round trip sits between the two.
func (m *Model) tabByID(id string) *TabModel {
	for _, t := range m.tabs {
		if t != nil && t.ID == id {
			return t
		}
	}
	return nil
}

// applyGitScanTimeout turns a never-answered discovery into something
// diagnosable rather than a keypress that silently did nothing.
//
// Local timer: it must NOT re-arm listenForMessages, unlike the response
// branch. Matched on the requested CWD AND gen, so a late tick from a
// superseded request cannot clear a live one — including a live request that
// asked about the exact same directory (see repoScanState.gen).
func (m *Model) applyGitScanTimeout(cwd, gen string) tea.Cmd {
	if m.repoScan.cwd != cwd || m.repoScan.gen != gen {
		return nil
	}
	m.repoScan = repoScanState{}
	m.setFlash("repo scan timed out")
	return m.flashCmd()
}
