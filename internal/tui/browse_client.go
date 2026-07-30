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
const browseTimeout = 8 * time.Second

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

// applyBrowseDir resumes the directory-browser state machine with the
// daemon's answer.
//
// Matched on BOTH echoed fields, not Path alone: two descents from one
// directory differ only in Child, and matching on Path would let the second
// request's answer land as though it belonged to the first (or vice versa).
func (m *Model) applyBrowseDir(resp ipc.BrowseDirRespPayload) tea.Cmd {
	if resp.Path != m.browse.path || resp.Child != m.browse.child {
		return nil
	}
	m.browse.pending = false

	// A failure is NOT an empty directory. Rendering it as one would show a
	// confidently wrong answer — an unreadable directory would look identical
	// to one that genuinely holds nothing, and the listing on screen (from
	// before this request) must not be clobbered by a scan that never
	// produced a replacement.
	if resp.Error != "" {
		m.browse.err = resp.Error
		return nil
	}
	m.browse.err = ""
	m.applyBrowseListing(resp.Resolved, resp.Entries, resp.Parent != "" || len(resp.Roots) > 0, m.browse.select_)
	return nil
}

// applyBrowseTimeout turns a never-answered request into something
// diagnosable rather than a keypress that silently leaves the browser
// spinning.
//
// Local timer: it must NOT re-arm listenForMessages, unlike the response
// branch. Matched on the requested (path, child) so a late tick from a
// superseded request cannot cancel a live one.
func (m *Model) applyBrowseTimeout(path, child string) tea.Cmd {
	if m.browse.path != path || m.browse.child != child {
		return nil
	}
	m.browse.pending = false
	m.browse.err = "directory listing timed out"
	return nil
}
