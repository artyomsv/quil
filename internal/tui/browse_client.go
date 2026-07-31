package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// browseDirMsg carries one directory-listing response. Gen echoes the
// requesting message's ID — see browseState.gen for why a response needs one.
type browseDirMsg struct {
	Resp ipc.BrowseDirRespPayload
	Gen  string
}

// browseTimeoutMsg fires when a browse request went unanswered.
type browseTimeoutMsg struct{ path, child, gen string }

// browseTimeout bounds one directory-browser round trip from the client
// side. Matches gitScanTimeout rather than the 3 s session-picker timeout:
// this request can cross the same ssh link, where a first round trip after
// an idle period pays a TCP handshake and, on Windows, a full ssh
// authentication with no ControlMaster to amortise it.
//
// A var rather than a const purely so the test binary can shorten it, in the
// same spirit as clipboardReadText in this package: no branch, no build tag,
// no test detection, and production reads the value declared here. tea.Tick's
// Cmd blocks for its whole duration, and the test helper that drains a
// tea.Batch runs its children synchronously, so every test that issues a
// browse request otherwise sleeps the full eight seconds for a timer it is not
// asserting on. It is overridden ONCE, from TestMain — these tests run in
// parallel, so mutating it mid-run would be a genuine data race.
var browseTimeout = 8 * time.Second

// browseState tracks an in-flight directory-browser request.
//
// Unlike repoScanState, this has an explicit pending flag rather than using
// an empty path as "nothing in flight": "" is itself a valid request (the
// daemon's default CWD), so it cannot double as the zero-value sentinel.
type browseState struct {
	path    string // echoed back; identifies WHAT was asked, together with child
	child   string // echoed back; the other half of what was asked
	pending bool
	err     string
	select_ string // entry to highlight when the listing lands ("up" navigation)
	// gen identifies WHICH request this is, on top of (path, child)
	// identifying what it asked about. The pre-fill chain and user navigation
	// both funnel through requestBrowseDir, and a repeated (path, child) pair
	// is reachable in earnest — descending into the same directory twice,
	// or the chain re-trying a candidate — so content alone cannot always
	// tell two overlapping requests apart. Concretely: request N's timeout
	// tick is scheduled 8s out; if request N+1 re-asks about the exact same
	// (path, child) inside that window, N's tick would otherwise match N+1's
	// still-live slot and report a false timeout out from under it. gen (from
	// nextReqGen) is carried in the wire message's ID (echoed back verbatim by
	// respondTo) and in the timeout closure, so both are matched against the
	// CURRENT request's instance, not just its content. Mirrors
	// repoScanState.gen / Model.clientGen.
	gen string
}

// requestBrowseDir asks the daemon to list one directory, descending into
// child first if set. The daemon is the only side that can answer honestly:
// os.ReadDir here would list THIS machine's filesystem, which is the wrong
// disk whenever the daemon is remote — the same reasoning as requestGitRepos.
func (m *Model) requestBrowseDir(path, child, selectName string) tea.Cmd {
	gen := m.nextReqGen()
	m.browse = browseState{path: path, child: child, pending: true, select_: selectName, gen: gen}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgBrowseDirReq, ipc.BrowseDirReqPayload{Path: path, Child: child})
			if err != nil {
				log.Printf("browse dir: encode: %v", err)
				return nil
			}
			msg.ID = gen
			m.client.Send(msg)
			return nil
		},
		browseTimeoutCmd(path, child, gen),
	)
}

func browseTimeoutCmd(path, child, gen string) tea.Cmd {
	return tea.Tick(browseTimeout, func(time.Time) tea.Msg {
		return browseTimeoutMsg{path: path, child: child, gen: gen}
	})
}

// browseOutcome reports what applyBrowseDir did with one response.
//
// Returned rather than stored: it is the ONE piece of knowledge only
// applyBrowseDir has — whether the echoed (Path, Child) matched — and it is
// consumed in the same breath as it is produced (pending is false either way
// afterwards). A caller that needed it later would have to re-run the
// comparison, and that comparison must exist in exactly one place.
type browseOutcome int

const (
	browseDropped browseOutcome = iota // stale answer; nothing changed
	browseFilled                       // listing replaced with this answer
	browseFailed                       // matched, but the daemon reported an error
)

// applyBrowseDir resumes the directory-browser state machine with the
// daemon's answer, and reports what it observed.
//
// Matched on Path, Child, AND gen: two descents from one directory differ
// only in Child, so Path alone would let the second request's answer land as
// though it belonged to the first. gen is needed on top of that pair — see
// browseState.gen — for the case where two DIFFERENT requests ask about the
// exact same (Path, Child), which content matching alone cannot tell apart.
//
// Deliberately holds no policy of its own — what a failure or a fresh listing
// MEANS to the setup dialog (the pre-fill chain) is the dialog's business, and
// lives in applyBrowseResponse.
func (m *Model) applyBrowseDir(resp ipc.BrowseDirRespPayload, gen string) browseOutcome {
	if resp.Path != m.browse.path || resp.Child != m.browse.child || gen != m.browse.gen {
		return browseDropped
	}
	m.browse.pending = false

	// A failure is NOT an empty directory. Rendering it as one would show a
	// confidently wrong answer — an unreadable directory would look identical
	// to one that genuinely holds nothing, and the listing on screen (from
	// before this request) must not be clobbered by a scan that never
	// produced a replacement.
	if resp.Error != "" {
		m.browse.err = resp.Error
		return browseFailed
	}
	m.browse.err = ""

	// Recorded for "up" navigation, which must use the daemon's own answer
	// rather than a filepath.Dir computed against this machine's separators.
	m.cwdBrowseParent = resp.Parent
	m.cwdBrowseRoots = resp.Roots

	// A capped listing is a PARTIAL answer, and rendering it like a complete
	// one is the same confidently-wrong failure as rendering an error as an
	// empty directory: a user who scrolls a huge directory without finding a
	// folder concludes it is not there.
	m.cwdBrowseTruncated = resp.Truncated
	// Same reasoning, for the list the user sees after navigating up. A drive
	// that stopped answering is exactly a drive whose absence looks like it was
	// never there.
	m.cwdBrowseRootsTruncated = resp.RootsTruncated

	m.applyBrowseListing(resp.Resolved, resp.Entries, resp.Parent != "" || len(resp.Roots) > 0, m.browse.select_)
	return browseFilled
}

// applyBrowseTimeout turns a never-answered request into something
// diagnosable rather than a keypress that silently leaves the browser
// spinning.
//
// Local timer: it must NOT re-arm listenForMessages, unlike the response
// branch. Matched on the requested (path, child), gen, AND pending.
//
// pending and gen fix two DIFFERENT gaps and neither can stand in for the
// other. pending is what tells a request's OWN late tick — firing after that
// SAME request already succeeded — from a tick that still means something:
// applyBrowseDir cannot clear path/child back to a zero value on a match
// (unlike repoScanState, "" is a valid Path — the daemon's default CWD — so
// clearing to zero would make a genuinely idle browser indistinguishable from
// one mid-request), so the request's own timeout tick, scheduled alongside
// the send in requestBrowseDir, is still sitting there after a successful
// match and would otherwise fire 8s later and overwrite the listing it just
// delivered with a stale "timed out". gen is what tells a DIFFERENT request's
// tick apart from the CURRENT one when both happen to share the same
// (path, child) — the pre-fill chain and user navigation both funnel through
// requestBrowseDir, and a repeated pair is reachable in earnest (see
// browseState.gen). Without gen, request N's tick firing while request N+1 —
// for the identical (path, child) — is still genuinely pending would
// incorrectly report N+1 as timed out.
func (m *Model) applyBrowseTimeout(path, child, gen string) tea.Cmd {
	if !m.browse.pending || m.browse.path != path || m.browse.child != child || m.browse.gen != gen {
		return nil
	}
	m.browse.pending = false
	m.browse.err = "directory listing timed out"
	return nil
}
