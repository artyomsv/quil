package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// overlayAsk is what createOverlay asked the daemon for: which tool, in which
// folder (as this client named it, before the daemon resolved symlinks).
type overlayAsk struct {
	plugin string
	cwd    string
}

// requestOverlayDir asks the tab's daemon whether cwd is still a directory
// before a directory-scoped overlay (yazi) is created there.
//
// The check exists because the create cannot refuse on its own: the daemon's
// create path falls back to a default directory for a CWD it cannot use, so a
// pane still reporting a deleted folder would open yazi somewhere unrelated,
// after the old overlay had already been destroyed. The git tools never had
// this gap — their candidates come from discovery, which only names
// repositories that exist. Asked of the DAEMON, like that discovery, because
// a stat here reads the wrong machine for a remote host.
//
// The request takes the same repoScan slot the git discovery uses, so whichever
// overlay key was pressed last wins: a git scan still in flight is superseded
// and its late answer or timeout is dropped by the gen check, instead of
// replacing the yazi the user asked for afterwards.
func (m *Model) requestOverlayDir(dest, cwd, tabID, plugin string) tea.Cmd {
	gen := m.nextReqGen()
	m.repoScan = repoScanState{cwd: cwd, tabID: tabID, purpose: repoScanOverlayDir, plugin: plugin, gen: gen}
	msg, err := ipc.NewMessage(ipc.MsgDirsExistReq, ipc.DirsExistReqPayload{Paths: []string{cwd}})
	if err != nil {
		log.Printf("overlay dir check: encode: %v", err)
		m.repoScan = repoScanState{}
		return nil
	}
	// respondTo echoes the ID verbatim; it is the only correlator, since a
	// dirs_exist answer carries no content key (see DirsExistRespPayload).
	msg.ID = gen
	// Stamped on the Update goroutine for the tab's daemon, which is where the
	// overlay will be created — the same reason requestGitRepos stamps.
	stampDest(msg, dest)
	return tea.Batch(
		func() tea.Msg {
			m.client.Send(msg)
			return nil
		},
		gitScanTimeoutCmd(cwd, gen),
	)
}

// isOverlayDirAnswer reports whether a dirs_exist answer belongs to the
// pending overlay folder check rather than to the setup dialog's recent list.
// Both share the response type; the generation tells them apart.
func (m *Model) isOverlayDirAnswer(gen string) bool {
	return gen != "" && gen == m.repoScan.gen && m.repoScan.purpose == repoScanOverlayDir
}

// applyOverlayDir resumes the overlay state machine once the daemon has said
// whether the folder exists. A folder that is gone, or a check that failed,
// is reported and changes nothing: the old overlay stays where it is.
func (m *Model) applyOverlayDir(resp ipc.DirsExistRespPayload) tea.Cmd {
	cwd, tabID, plugin := m.repoScan.cwd, m.repoScan.tabID, m.repoScan.plugin
	m.repoScan = repoScanState{}

	if resp.Error != "" {
		log.Printf("overlay dir check: %s", resp.Error)
		m.setErrorFlash(plugin + " not opened: could not check the folder")
		return m.flashCmd()
	}
	// Resolved again rather than captured: the user may have closed the tab
	// while the check was in flight, and then so is the intent.
	tab := m.tabByID(tabID)
	if tab == nil {
		return nil
	}
	// Exact match against what was asked, as applyExistingDirs does: the
	// answer comes from a host the user may not control, and this path becomes
	// the overlay's spawn directory.
	if len(intersectOrdered(resp.Paths, []string{cwd})) == 0 {
		m.setErrorFlash(plugin + " not opened: folder not found")
		return m.flashCmd()
	}
	return m.resolveOverlay(tab, []string{cwd}, plugin)
}

// overlayRequestPending reports whether an overlay request for exactly this
// tab, folder and tool is still waiting on the daemon.
func (m *Model) overlayRequestPending(tabID, cwd, plugin string) bool {
	s := m.repoScan
	if s.gen == "" || (s.purpose != repoScanOverlay && s.purpose != repoScanOverlayDir) {
		return false
	}
	return s.tabID == tabID && s.cwd == cwd && s.plugin == plugin
}

// dropPendingOverlayScan forgets an overlay request still waiting on the
// daemon — a git discovery or a yazi folder check — so its answer and its
// timeout are both dropped by the gen check when they arrive. The setup
// dialog's pick-list scan shares the slot and is left alone: no overlay key
// can be pressed while that dialog owns the keyboard.
//
// gen, not purpose, says whether anything is in flight: repoScanOverlay is
// the zero value, so an idle slot also reads as an overlay purpose.
func (m *Model) dropPendingOverlayScan() {
	s := m.repoScan
	if s.gen == "" || (s.purpose != repoScanOverlay && s.purpose != repoScanOverlayDir) {
		return
	}
	m.repoScan = repoScanState{}
}
