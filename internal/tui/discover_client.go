package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// gitReposMsg carries one git-discovery response.
type gitReposMsg struct{ Resp ipc.GitReposRespPayload }

// gitScanTimeoutMsg fires when a discovery request went unanswered.
type gitScanTimeoutMsg struct{ cwd string }

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
	cwd     string // echoed back by the daemon; the staleness key
	tabID   string // the tab that asked (repoScanOverlay only), resolved again on arrival
	purpose repoScanPurpose
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
	m.repoScan = repoScanState{cwd: cwd, tabID: tabID, purpose: purpose}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: cwd})
			if err != nil {
				log.Printf("git discovery: encode: %v", err)
				return nil
			}
			m.client.Send(msg)
			return nil
		},
		gitScanTimeoutCmd(cwd),
	)
}

func gitScanTimeoutCmd(cwd string) tea.Cmd {
	return tea.Tick(gitScanTimeout, func(time.Time) tea.Msg {
		return gitScanTimeoutMsg{cwd: cwd}
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
// user is no longer looking at. Matched on the echoed CWD, the same staleness
// contract the browse and session listings use.
func (m *Model) applyGitRepos(resp ipc.GitReposRespPayload) tea.Cmd {
	if resp.CWD != m.repoScan.cwd {
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
// branch. Matched on the requested CWD so a late tick from a superseded request
// cannot clear a live one.
func (m *Model) applyGitScanTimeout(cwd string) tea.Cmd {
	if m.repoScan.cwd != cwd {
		return nil
	}
	m.repoScan = repoScanState{}
	m.setFlash("repo scan timed out")
	return m.flashCmd()
}
