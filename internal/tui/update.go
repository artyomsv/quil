package tui

import (
	"log"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/update"
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

// maybeShowUpdateNotice opens the once-per-version startup dialog.
// Priority: the migration dialog (blocking) and any interactive dialog win;
// only the informational disclaimer yields (spec: migration > update notice
// > disclaimer — the disclaimer reappears next launch).
func (m *Model) maybeShowUpdateNotice() {
	if !updateAvailable(m.updateInfo, m.version) {
		return
	}
	if m.dialog != dialogNone && m.dialog != dialogDisclaimer {
		return
	}
	if update.LoadNotifiedVersion(config.UpdateNotifiedPath()) == m.updateInfo.LatestVersion {
		return
	}
	m.dialog = dialogUpdateNotice
	m.dialogCursor = 0
	if err := update.SaveNotifiedVersion(config.UpdateNotifiedPath(), m.updateInfo.LatestVersion); err != nil {
		log.Printf("save update notified marker: %v", err)
	}
}

// handleUpdateNoticeKey drives the two-button startup notice
// (OK / Update now), mirroring the disclaimer's cursor idiom.
func (m Model) handleUpdateNoticeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	case "left", "right", "tab":
		m.dialogCursor = 1 - m.dialogCursor
		return m, nil
	case "enter":
		if m.dialogCursor == 1 {
			return m.handleUpdateAction()
		}
		m.dialog = dialogNone
		m.dialogCursor = 0
		return m, tea.ClearScreen
	}
	return m, nil
}

// renderUpdateNoticeDialog renders the once-per-version startup notice.
func (m Model) renderUpdateNoticeDialog() string {
	info := m.updateInfo
	if info == nil {
		return ""
	}
	var b strings.Builder
	title := dialogTitle.Render("Update available")
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, title))
	b.WriteString("\n\n")
	b.WriteString(dialogNormal.Render("  Installed: v" + m.version))
	b.WriteByte('\n')
	b.WriteString(dialogNormal.Render("  Latest:    v" + info.LatestVersion))
	b.WriteString("\n\n")
	switch {
	case info.StagedVersion == info.LatestVersion:
		b.WriteString(dialogSubtle.Render("  Downloaded and staged — applies on next launch."))
	case !info.InstallWritable:
		b.WriteString(dialogSubtle.Render("  Install dir not writable — update manually:"))
	default:
		b.WriteString(dialogSubtle.Render("  Will download in the background."))
	}
	b.WriteByte('\n')
	if info.ReleaseURL != "" {
		b.WriteString(dialogSubtle.Render("  " + info.ReleaseURL))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	okLabel := "  OK  "
	updateLabel := "  Update now  "
	okStyle, updateStyle := dialogSelected, dialogNormal
	if m.dialogCursor == 1 {
		okStyle, updateStyle = dialogNormal, dialogSelected
	}
	buttons := okStyle.Render(okLabel) + "    " + updateStyle.Render(updateLabel)
	b.WriteString(lipgloss.PlaceHorizontal(dialogWidth, lipgloss.Center, buttons))
	return b.String()
}
