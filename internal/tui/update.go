package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/version"
)

// stageUpdateRespMsg carries the daemon's answer to MsgStageUpdateReq.
type stageUpdateRespMsg struct {
	Resp ipc.StageUpdateRespPayload
}

// updateAvailable reports whether info announces a release strictly newer
// than the running TUI. False for dev builds (unparseable current) — the
// pipeline is daemon-gated too, this is the belt-and-suspenders TUI gate.
func updateAvailable(info *ipc.UpdateInfo, current string) bool {
	if info == nil || info.LatestVersion == "" {
		return false
	}
	cmp, err := version.Compare(info.LatestVersion, current)
	return err == nil && cmp > 0
}

// updateStatusSegment renders the persistent status-bar reminder:
// "↑ v1.37.0" (known, not staged) / "↑ v1.37.0 ready" (staged — applies on
// next launch). Empty when up to date or no info.
func updateStatusSegment(info *ipc.UpdateInfo, current string) string {
	if !updateAvailable(info, current) {
		return ""
	}
	if info.StagedVersion == info.LatestVersion {
		return "↑ v" + info.LatestVersion + " ready"
	}
	return "↑ v" + info.LatestVersion
}

// aboutUpdateLabel is the dynamic F1 → About row for updates.
func aboutUpdateLabel(info *ipc.UpdateInfo, current string) string {
	if !updateAvailable(info, current) {
		return "Check for updates (up to date)"
	}
	if !info.InstallWritable {
		return "Update available: v" + info.LatestVersion + " (manual install)"
	}
	if info.StagedVersion == info.LatestVersion {
		return "Update to v" + info.LatestVersion + " (staged — applies on restart)"
	}
	return "Update to v" + info.LatestVersion + " (download)"
}

// handleUpdateAction is the Enter action for the About update row and the
// startup notice's "Update now" button. Branches: up to date → flash;
// unwritable → flash pointing at the release page; staged → apply confirm;
// otherwise → on-demand stage request to the daemon.
func (m Model) handleUpdateAction() (tea.Model, tea.Cmd) {
	info := m.updateInfo
	m.dialog = dialogNone
	if !updateAvailable(info, m.version) {
		m.setFlash("quil is up to date (v" + m.version + ")")
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if !info.InstallWritable {
		m.setFlash("v" + info.LatestVersion + " available — install dir not writable, see " + info.ReleaseURL)
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if info.StagedVersion == info.LatestVersion {
		m.dialog = dialogConfirm
		m.confirmKind = confirmKindApplyUpdate
		m.confirmID = ""
		m.confirmName = info.LatestVersion
		m.dialogCursor = 0
		return m, nil
	}
	// Nothing staged yet ([update] auto = false, or the daily tick hasn't
	// run): ask the daemon to stage now. Response lands as
	// stageUpdateRespMsg; the refreshed broadcast updates m.updateInfo.
	m.setFlash("downloading update v" + info.LatestVersion + "…")
	if m.client != nil {
		req, err := ipc.NewMessage(ipc.MsgStageUpdateReq, nil)
		if err != nil {
			log.Printf("stage update: marshal: %v", err)
		} else if err := m.client.Send(req); err != nil {
			log.Printf("stage update: send: %v", err)
		}
	}
	return m, tea.Batch(tea.ClearScreen, m.flashCmd())
}
