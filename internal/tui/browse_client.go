package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// browseDirMsg carries one directory-listing response.
type browseDirMsg struct{ Resp ipc.BrowseDirRespPayload }

// browseTimeoutMsg fires when a browse request went unanswered.
type browseTimeoutMsg struct{ path, child string }

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
	path    string // echoed back; half of the staleness key
	child   string // echoed back; the other half
	pending bool
	err     string
	select_ string // entry to highlight when the listing lands ("up" navigation)
}

// requestBrowseDir asks the daemon to list one directory, descending into
// child first if set. The daemon is the only side that can answer honestly:
// os.ReadDir here would list THIS machine's filesystem, which is the wrong
// disk whenever the daemon is remote — the same reasoning as requestGitRepos.
func (m *Model) requestBrowseDir(path, child, selectName string) tea.Cmd {
	m.browse = browseState{path: path, child: child, pending: true, select_: selectName}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgBrowseDirReq, ipc.BrowseDirReqPayload{Path: path, Child: child})
			if err != nil {
				log.Printf("browse dir: encode: %v", err)
				return nil
			}
			m.client.Send(msg)
			return nil
		},
		browseTimeoutCmd(path, child),
	)
}

func browseTimeoutCmd(path, child string) tea.Cmd {
	return tea.Tick(browseTimeout, func(time.Time) tea.Msg {
		return browseTimeoutMsg{path: path, child: child}
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
// Matched on BOTH echoed fields, not Path alone: two descents from one
// directory differ only in Child, and matching on Path would let the second
// request's answer land as though it belonged to the first (or vice versa).
//
// Deliberately holds no policy of its own — what a failure or a fresh listing
// MEANS to the setup dialog (the pre-fill chain) is the dialog's business, and
// lives in applyBrowseResponse.
func (m *Model) applyBrowseDir(resp ipc.BrowseDirRespPayload) browseOutcome {
	if resp.Path != m.browse.path || resp.Child != m.browse.child {
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

	m.applyBrowseListing(resp.Resolved, resp.Entries, resp.Parent != "" || len(resp.Roots) > 0, m.browse.select_)
	return browseFilled
}

// applyBrowseTimeout turns a never-answered request into something
// diagnosable rather than a keypress that silently leaves the browser
// spinning.
//
// Local timer: it must NOT re-arm listenForMessages, unlike the response
// branch. Matched on the requested (path, child) AND pending — the key alone
// is not enough here, unlike gitScanTimeoutMsg's match against repoScanState:
// that reference clears its key back to the zero value on a match, so a late
// tick can no longer find it. applyBrowseDir cannot do the same, because ""
// is a valid Path (the daemon's default CWD) and clearing to the zero value
// would make a genuinely idle browser indistinguishable from one mid-request.
// applyBrowseDir therefore leaves path/child in place after a match, which
// means the request's own timeout tick — scheduled alongside the send in
// requestBrowseDir — is still sitting there and would otherwise fire 8s
// later and overwrite a just-applied successful listing with a stale
// "timed out". Gating on pending closes that: a match already cleared it, so
// the same request's later tick is a no-op.
func (m *Model) applyBrowseTimeout(path, child string) tea.Cmd {
	if !m.browse.pending || m.browse.path != path || m.browse.child != child {
		return nil
	}
	m.browse.pending = false
	m.browse.err = "directory listing timed out"
	return nil
}
