package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// recentDirsMsg carries one existence-check response. Gen echoes the requesting
// message's ID — see recentScanState.gen.
type recentDirsMsg struct {
	Resp ipc.DirsExistRespPayload
	Gen  string
}

// recentScanTimeoutMsg fires when an existence check went unanswered.
type recentScanTimeoutMsg struct{ gen string }

// recentScanTimeout bounds one existence check from the client side.
//
// Sized like gitScanTimeout and for the same reason: this request can cross
// ssh, where a first round trip after an idle period pays a TCP handshake and,
// on Windows, a full authentication. Deliberately SHORTER than the daemon's own
// browseTimeout (10 s), which is why the daemon gives this handler its own
// single-flight slot — the handover below fires while the daemon may still hold
// that slot, and a shared guard would have it reject the very browse requests
// this timeout goes on to make.
//
// A var, not a const, purely so the test binary can shorten it — same TestMain
// override as browseTimeout, gitScanTimeout and kubeScanTimeout.
var recentScanTimeout = 8 * time.Second

// recentScanState tracks an in-flight existence check.
//
// The zero value means "nothing in flight". Like kubeScanState and unlike
// browseState it needs no separate pending flag: the request's content key is a
// path LIST, which makes a poor staleness key, so gen is the whole identity and
// nextReqGen never returns "".
type recentScanState struct {
	gen string
	// asked is the list this request sent. Kept so the answer can be
	// intersected against it: the daemon is a host the user may not control,
	// and the surviving paths must be a SUBSET of what was asked about, not
	// whatever the far side chose to name. paths[0] becomes the pane's spawn
	// directory and is persisted into the local recent list on submit, so a
	// substituted entry is not merely cosmetic.
	asked []string
}

// requestExistingDirs asks the daemon which remembered directories still exist.
//
// Filtered here with os.Stat until now, which reads the machine drawing the UI:
// against a remote daemon every server path failed and the pick list rendered
// silently empty — indistinguishable from a feature that had never been used,
// because structurally nothing had failed. No error was shown because no error
// occurred.
//
// Used in local mode too, deliberately. The answer is identical when the daemon
// is local, and a path exercised only by remote sessions is one that rots.
func (m *Model) requestExistingDirs(paths []string) tea.Cmd {
	gen := m.nextReqGen()
	m.recentScan = recentScanState{gen: gen, asked: paths}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgDirsExistReq, ipc.DirsExistReqPayload{Paths: paths})
			if err != nil {
				log.Printf("recent locations: encode: %v", err)
				return nil
			}
			// The daemon's respondTo echoes ID back verbatim; it is the whole
			// correlator here, since the request carries no usable content key.
			msg.ID = gen
			m.client.Send(msg)
			return nil
		},
		recentScanTimeoutCmd(gen),
	)
}

func recentScanTimeoutCmd(gen string) tea.Cmd {
	return tea.Tick(recentScanTimeout, func(time.Time) tea.Msg {
		return recentScanTimeoutMsg{gen: gen}
	})
}

// applyExistingDirs installs the surviving directories as the quick pick, or
// hands over to the browser when there is nothing left to offer.
//
// A failure falls through to the browser too, NOT to an empty list: the browser
// always works (it is an RPC against the same daemon), so this degrades to "you
// have to navigate" rather than to a wrong answer or a dead dialog. Reporting a
// failed check as "none of your directories exist" would be a wrong answer
// stated confidently, which is the failure this whole phase exists to remove.
//
// Dropped if the setup dialog has since closed, matching applyGitReposPickList:
// the generation still matches after Esc, so without this the browser chain
// would fire for a dialog nobody is in and mutate the pick-list fields outside
// it.
func (m *Model) applyExistingDirs(resp ipc.DirsExistRespPayload, gen string) tea.Cmd {
	if gen == "" || gen != m.recentScan.gen {
		return nil
	}
	asked := m.recentScan.asked
	m.recentScan = recentScanState{}
	if m.dialog != dialogCreatePaneSetup {
		return nil
	}

	if resp.Error != "" {
		log.Printf("recent locations: %s", resp.Error)
		return m.initSetupBrowser()
	}

	// Keep only what was actually asked about, then cap. The daemon bounds the
	// REQUEST, but a bound enforced only by the sender is not a bound once the
	// sender is a host the user may not control — the same reasoning
	// applyKubeContexts states, and it bites harder here than on a rendered
	// list: paths[0] becomes cwdBrowseDir, the pane's spawn directory, and is
	// written into the local recent list when the dialog is submitted. An
	// unfiltered answer would let the far side both name a directory the user
	// never visited and, with a long enough list, wedge the renderer — the pick
	// list has no visible window, unlike the browser.
	paths := intersectOrdered(resp.Paths, asked)
	if len(paths) > recentCWDMax {
		paths = paths[:recentCWDMax]
	}
	if len(paths) == 0 {
		return m.initSetupBrowser()
	}
	m.recentCandidates = paths
	// Routed through onSetupCWDChanged like every other site that commits a
	// browsed directory — reachable on dialog re-entry, where a prior
	// plugin's worktree choice would otherwise still be sitting in
	// m.selectedWorktree when this candidate list lands. p is nil —
	// onSetupCWDChanged never reads it, and a m.pluginRegistry.Get lookup
	// here panics on the nil registry recentClientModel (and this file's
	// other tests) deliberately build, since they exercise this
	// response-handling logic in isolation from the rest of the dialog.
	m.onSetupCWDChanged(nil, paths[0])
	m.cwdBrowseCursor = 0
	return nil
}

// applyExistingDirsTimeout turns a never-answered check into the browser rather
// than an empty field.
//
// Local timer: it must NOT re-arm listenForMessages, unlike the response
// branch. Same dialog-closed guard as applyExistingDirs, and for a sharper
// reason — this fires unconditionally 8 s after the request, so an abandoned
// dialog is the COMMON case for it rather than the exotic one.
func (m *Model) applyExistingDirsTimeout(gen string) tea.Cmd {
	if gen == "" || gen != m.recentScan.gen {
		return nil
	}
	m.recentScan = recentScanState{}
	if m.dialog != dialogCreatePaneSetup {
		return nil
	}
	return m.initSetupBrowser()
}

// intersectOrdered returns the members of got that appear in allowed,
// preserving got's order and dropping duplicates.
//
// Compared exactly, never through pathEqual: these strings made a round trip
// and came back, so the only claim worth trusting is that a returned value is
// byte-identical to one that was sent. Case-folding a remote answer would let a
// case-sensitive host hand back an entry the local list does not contain.
func intersectOrdered(got, allowed []string) []string {
	if len(got) == 0 || len(allowed) == 0 {
		return nil
	}
	permitted := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		permitted[a] = struct{}{}
	}
	out := make([]string, 0, len(got))
	seen := make(map[string]struct{}, len(got))
	for _, g := range got {
		if _, ok := permitted[g]; !ok {
			continue
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return out
}
