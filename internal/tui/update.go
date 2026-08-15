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
// than the running TUI. False for dev builds (unparseable current) and
// whenever this build has the pipeline disabled (dev/debug variants) — the
// pipeline is daemon-gated too, this is the belt-and-suspenders TUI gate.
func updateAvailable(info *ipc.UpdateInfo, current string) bool {
	if !version.UpdatesEnabled() {
		return false
	}
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
//
// remote says the daemon behind the ACTIVE project is on another host. Its
// announcement describes that host's releases and its staging dir, while
// applying an update swaps the binaries on THIS machine — so the row says so
// rather than offering an action that would cross machines, matching the
// refusal in handleUpdateAction and the one maybeShowUpdateNotice already made
// for the startup notice.
func aboutUpdateLabel(info *ipc.UpdateInfo, current string, remote bool) string {
	if !version.UpdatesEnabled() {
		return "Updates disabled (dev build)"
	}
	if remote {
		return "Updates apply locally (remote project active)"
	}
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
// startup notice's "Update now" button. Every branch that acts on a version
// re-checks with the daemon first, because the only version this Model knows
// came from a broadcast the daemon refreshes once a day: up to date → re-check
// (a report of "nothing newer" does not mean a check ran this session); staged
// → re-check, then apply whatever is newest; not staged → stage. Only the
// unwritable branch answers from the broadcast, and it offers no action.
func (m Model) handleUpdateAction() (tea.Model, tea.Cmd) {
	info := m.activeUpdateInfo()
	// Captured BEFORE the dialog is closed below. The apply confirm now opens
	// from the daemon's answer, which can land long after this press, so Esc on
	// it must return where the user actually came from: the About menu when
	// they are still standing in it, and the panes when the confirm arrived on
	// its own. Painting About over a pane the user went back to working in is
	// the same "dialog they never opened" complaint from the other direction.
	m.applyConfirmReturn = dialogNone
	if m.dialog == dialogAbout {
		m.applyConfirmReturn = dialogAbout
	}
	m.dialog = dialogNone
	if !version.UpdatesEnabled() {
		m.setFlash("updates are disabled in dev builds")
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	// BEFORE the pendingApplyVer reset below, and that ordering is the whole
	// point: this press is refused, so it must leave no trace. Clearing the
	// intent here would silently discard the FIRST press's "apply when this
	// answers" — the user asked to apply, waited out the download, and got a
	// flash.
	if m.updateReqInFlight {
		// A request is unanswered. Sending a second one is not merely wasteful:
		// while the first is DOWNLOADING a newer release, the daemon answers the
		// second from stageRelease's CAS ("staging already in progress") within
		// milliseconds — and that fast failure used to be read as "the check
		// failed", opening the apply confirm for the OLD staged version. Pressing
		// y there installs the intermediate release and abandons the download
		// that was fetching the newest: the exact loop this whole change exists
		// to end, re-created by an impatient second press.
		m.setFlash("already checking — hold on…")
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	// Cleared here and set only by the staged branch below: a leftover from an
	// earlier press would make an ordinary download open the apply confirm the
	// user did not ask for.
	m.pendingApplyVer = ""
	if m.remoteModeFor(m.activeDest()) {
		// Every value on this row came from the active project's daemon, and
		// the request below would go back to it — but applying swaps the
		// binaries on THIS machine, out of THIS machine's staging dir. Staging
		// on the far host and then installing locally is a wrong-machine
		// action in both directions, so it is refused rather than performed
		// (the startup notice refuses on the same grounds). It matters more
		// now that a successful stage continues straight into the apply
		// confirm: the confirm would name a version this machine never staged.
		m.setFlash("updates apply to this machine — switch to a local project first")
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if !updateAvailable(info, m.version) {
		// "Up to date" per the last broadcast doesn't mean a check ever ran
		// this session — ask the daemon to check now. stageOnDemand
		// re-checks GitHub and answers Error == "already up to date" when
		// there's truly nothing newer; stageUpdateRespMsg's handler turns
		// that into a friendly "quil is up to date" flash rather than
		// "update failed: ...".
		m.setFlash("checking for updates…")
		m.sendStageUpdateReq()
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if !info.InstallWritable {
		m.setFlash("v" + info.LatestVersion + " available — install dir not writable, see " + info.ReleaseURL)
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	if info.StagedVersion == info.LatestVersion {
		// Something is staged — but "staged" here means "staged as of the
		// daemon's last check", and that check runs 1 min after daemon start
		// and every 24 h after. A release published inside that window is
		// invisible to this Model, so applying straight from it installs a
		// version that is no longer the newest and leaves the user to repeat
		// the whole cycle after the restart. Ask the daemon what is actually
		// latest; it answers AlreadyStaged when this stage is still it, and
		// stages the newer one when it is not.
		//
		// pendingApplyVer records that this press meant APPLY (the "Update
		// to vX (download)" row sends the same request and must keep its
		// flash-only outcome) and carries the fallback: a check that cannot
		// reach GitHub must still be able to install what is already on disk.
		m.pendingApplyVer = info.LatestVersion
		m.setFlash("checking for a newer version…")
		m.sendStageUpdateReq()
		return m, tea.Batch(tea.ClearScreen, m.flashCmd())
	}
	// Nothing staged yet ([update] auto = false, or the daily tick hasn't
	// run): ask the daemon to stage now. Response lands as
	// stageUpdateRespMsg; the refreshed broadcast updates m.updateInfos.
	m.setFlash("downloading update v" + info.LatestVersion + "…")
	m.sendStageUpdateReq()
	return m, tea.Batch(tea.ClearScreen, m.flashCmd())
}

// sendStageUpdateReq fires MsgStageUpdateReq at the daemon (no-op without a
// live client, e.g. in tests). Shared by the up-to-date recheck, the
// staged-apply recheck, and the not-staged-yet download branch.
//
// Pointer receiver: it sets updateReqInFlight, which handleUpdateAction's
// caller must see.
func (m *Model) sendStageUpdateReq() {
	if m.client == nil {
		return
	}
	req, err := ipc.NewMessage(ipc.MsgStageUpdateReq, nil)
	if err != nil {
		log.Printf("stage update: marshal: %v", err)
		return
	}
	if err := m.client.Send(req); err != nil {
		log.Printf("stage update: send: %v", err)
		return
	}
	m.updateReqInFlight = true
}

// openAboutDialog opens F1 → About and asks the daemon for a fresh release
// check on the way in, so the update row is describing GitHub rather than
// whatever the daily tick last found. Used by the F1 key and the palette's
// About command — NOT by the Esc paths that return here from a sub-dialog,
// which are back-navigation inside one visit and would otherwise re-request on
// every step out of Settings.
func (m Model) openAboutDialog() (tea.Model, tea.Cmd) {
	m.dialog = dialogAbout
	m.dialogCursor = 0
	m.sendUpdateCheckReq()
	return m, tea.ClearScreen
}

// sendUpdateCheckReq asks the daemon to re-check GitHub without downloading
// anything, so the About row describes the current release rather than
// whatever the daily tick last found. Fire-and-forget: the answer arrives as
// the refreshed "update" key on the next workspace_state broadcast, and the
// daemon rate-limits the path (opening a dialog must not be able to spend the
// unauthenticated GitHub quota).
func (m Model) sendUpdateCheckReq() {
	// Skipped for a remote active project for the reason handleUpdateAction
	// refuses there: the answer would describe the far host's releases, and it
	// would spend that host's GitHub quota to tell this machine nothing.
	if m.client == nil || !version.UpdatesEnabled() || m.remoteModeFor(m.activeDest()) {
		return
	}
	req, err := ipc.NewMessage(ipc.MsgUpdateCheckReq, nil)
	if err != nil {
		log.Printf("update check: marshal: %v", err)
		return
	}
	if err := m.client.Send(req); err != nil {
		log.Printf("update check: send: %v", err)
	}
}

// applyStageUpdateResp turns the daemon's answer into the next step. The
// branch that matters is pendingApplyVer: the press it records asked to
// APPLY, so a success — whether the stage was already current or a newer
// release was downloaded just now — continues into the apply confirm for the
// version the daemon actually has, and a failure falls back to confirming the
// staged version rather than stranding the user on a flash.
func (m Model) applyStageUpdateResp(resp ipc.StageUpdateRespPayload) (Model, tea.Cmd) {
	pending := m.pendingApplyVer
	m.pendingApplyVer = ""
	m.updateReqInFlight = false

	// The version is remote-sourced under --remote and lands in a flash and a
	// dialog title, neither of which passes through a VT emulator.
	ver := sanitizeRemoteText(resp.Version)

	switch {
	case resp.Success && pending != "":
		return m.openApplyConfirm(ver, "")
	case resp.Success && resp.AlreadyStaged:
		m.setFlash("v" + ver + " is already staged — applies on next launch")
	case resp.Success:
		m.setFlash("update v" + ver + " staged — applies on next launch")
	case pending != "" && resp.CheckFailed:
		// ONLY a failed CHECK licenses this. The staged bytes are still on disk
		// and still newer than what is running, so the apply the user asked for
		// remains possible — it just cannot be confirmed as the newest, and the
		// reason rides the confirm itself because a flash is invisible behind a
		// dialog. Every other failure means the question was answered, or the
		// request never ran: falling back there would install an older stage
		// over a newer one that is mid-download.
		return m.openApplyConfirm(sanitizeRemoteText(pending), stageRespReason(resp))
	case resp.Error == "already up to date":
		// handleUpdateAction's "up to date" branch sends this request to force
		// a fresh check rather than trusting stale broadcast info; a
		// re-confirmed "up to date" is a normal outcome, not a failure. It also
		// reaches here WITH an apply intent when a staged release was yanked —
		// GitHub answered, and it said nothing is newer, so the honest reply is
		// this rather than a confirm contradicting its own detail line.
		m.setFlash("quil is up to date (v" + m.version + ")")
	default:
		m.setFlash("update failed: " + sanitizeRemoteText(resp.Error))
	}
	return m, m.flashCmd()
}

// stageRespReason renders why the newest-release check did not settle, for the
// apply confirm's detail line. Bounded and sanitized at render like every
// other confirmDetail — under --remote the text originates on a host the user
// may not control.
func stageRespReason(resp ipc.StageUpdateRespPayload) string {
	if resp.Error == "" {
		return "could not confirm this is the newest release"
	}
	return "newest-release check: " + resp.Error
}

// openApplyConfirm arms the apply-update confirm for ver, with an optional
// detail line explaining a check that did not settle.
//
// It REFUSES to replace an open dialog. Unlike the old immediate path this can
// fire long after the keypress that started it (a stage takes as long as the
// download takes), and by then the user may be part-way through something
// else — which replacing the dialog would discard. The flash plus the
// unchanged About row are enough to finish the job by hand.
func (m Model) openApplyConfirm(ver, detail string) (Model, tea.Cmd) {
	if ver == "" {
		// A success with no version is not something to act on blindly.
		m.setFlash("update staged — F1 → Update to apply")
		return m, m.flashCmd()
	}
	if m.dialog != dialogNone {
		m.setFlash("v" + ver + " staged — F1 → Update to apply")
		return m, m.flashCmd()
	}
	m.dialog = dialogConfirm
	m.confirmKind = confirmKindApplyUpdate
	// The worktree-delete fields outlive the dialog, so every confirm re-clears
	// them ON OPEN as well as on the way out — an armed toggle inherited here
	// is a deletion nobody was offered. This open is the one that used to sit
	// inline in handleUpdateAction; the reset moved with it.
	m.resetConfirmWorktrees()
	m.confirmID = ""
	m.confirmName = ver
	m.confirmDetail = detail
	m.dialogCursor = 0
	return m, nil
}

// updateInfoFor returns the release dest's daemon announced, or nil.
func (m *Model) updateInfoFor(dest string) *ipc.UpdateInfo { return m.updateInfos[dest] }

// activeUpdateInfo returns the announcement of the daemon behind the project
// the user is looking at — the only one whose version describes the panes on
// screen, and the one the status bar and About row must answer for.
func (m *Model) activeUpdateInfo() *ipc.UpdateInfo { return m.updateInfoFor(m.activeDest()) }

// noteWorkspaceState is the WorkspaceStateMsg entry point for update state:
// it records the announcement under the destination it arrived on and, ONLY on
// the first LOCAL broadcast after attach (see Model.sawFirstState), offers the
// once-per-version startup notice. Every later broadcast in the session (switch
// tab, create pane, ...) also carries the update key on WorkspaceStateMsg, and
// without this gate would reopen the notice mid-session — the status-bar
// segment and About row already cover discovery after startup.
//
// dest is the WHOLE answer to "may this announcement open the notice", and it
// has to be, because the notice applies a LOCAL staged update: asking
// RemoteMode() instead reads the ACTIVE PROJECT, which on the first broadcast
// of a `--remote` session is still nil — activeDest() answers "" and the guard
// is dead exactly once, at the only moment it is needed.
//
// A remote announcement also may not CONSUME the once-per-launch gate. It
// returns before sawFirstState is set, so whichever daemon happens to broadcast
// first cannot cost the local one its notice.
func (m *Model) noteWorkspaceState(update *ipc.UpdateInfo, dest string) {
	if m.updateInfos == nil {
		m.updateInfos = make(map[string]*ipc.UpdateInfo, 1)
	}
	m.updateInfos[dest] = update
	if m.remoteModeFor(dest) {
		return
	}
	if !m.sawFirstState {
		m.maybeShowUpdateNotice(dest)
		m.sawFirstState = true
	}
}

// maybeShowUpdateNotice opens the once-per-version startup dialog for the
// daemon at dest. Priority: the migration dialog (blocking) and any
// interactive dialog win; only the informational disclaimer yields (spec:
// migration > update notice > disclaimer — the disclaimer reappears next
// launch).
func (m *Model) maybeShowUpdateNotice(dest string) {
	// Never for a remote daemon: its announcement describes ITS staging dir,
	// while accepting the notice applies a LOCAL staged update. Beyond
	// offering the wrong action, showing it would also write the remote's
	// LatestVersion into this machine's notified-version marker and suppress
	// the genuine local notice for that version.
	if m.remoteModeFor(dest) {
		return
	}
	info := m.updateInfoFor(dest)
	if !updateAvailable(info, m.version) {
		return
	}
	if m.dialog != dialogNone && m.dialog != dialogDisclaimer {
		return
	}
	if update.LoadNotifiedVersion(config.UpdateNotifiedPath()) == info.LatestVersion {
		return
	}
	m.dialog = dialogUpdateNotice
	m.dialogCursor = 0
	if err := update.SaveNotifiedVersion(config.UpdateNotifiedPath(), info.LatestVersion); err != nil {
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
	info := m.activeUpdateInfo()
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
