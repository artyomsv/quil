package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/version"
)

func TestUpdateStatusSegment(t *testing.T) {
	cases := []struct {
		name    string
		info    *ipc.UpdateInfo
		current string
		want    string
	}{
		{"nil info", nil, "0.0.1", ""},
		{"up to date", &ipc.UpdateInfo{LatestVersion: "0.0.1"}, "0.0.1", ""},
		{"older latest (rollback)", &ipc.UpdateInfo{LatestVersion: "0.0.1"}, "0.0.2", ""},
		{"newer not staged", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "0.0.1", "↑ v0.0.2"},
		{"newer staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2"}, "0.0.1", "↑ v0.0.2 ready"},
		{"dev build current", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "dev", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateStatusSegment(tc.info, tc.current); got != tc.want {
				t.Errorf("updateStatusSegment = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseWorkspaceState_UpdateKey(t *testing.T) {
	raw := map[string]any{
		"active_tab": "tab-aaaaaaaa",
		"update": map[string]any{
			"latest_version":   "0.0.2",
			"release_url":      "https://example.invalid/r",
			"staged_version":   "0.0.2",
			"install_writable": true,
		},
	}
	state := parseWorkspaceState(raw)
	if state.Update == nil {
		t.Fatal("state.Update = nil, want parsed info")
	}
	if state.Update.LatestVersion != "0.0.2" || state.Update.StagedVersion != "0.0.2" ||
		state.Update.ReleaseURL != "https://example.invalid/r" || !state.Update.InstallWritable {
		t.Errorf("state.Update = %+v", state.Update)
	}

	if got := parseWorkspaceState(map[string]any{"active_tab": "t"}); got.Update != nil {
		t.Errorf("no update key: state.Update = %+v, want nil", got.Update)
	}
}

func TestAboutUpdateLabel(t *testing.T) {
	cases := []struct {
		name    string
		info    *ipc.UpdateInfo
		current string
		remote  bool
		want    string
	}{
		{"up to date", nil, "0.0.1", false, "Check for updates (up to date)"},
		{"staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}, "0.0.1", false, "Update to v0.0.2 (staged — applies on restart)"},
		{"not staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}, "0.0.1", false, "Update to v0.0.2 (download)"},
		{"unwritable", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "0.0.1", false, "Update available: v0.0.2 (manual install)"},
		// The announcement is the far host's; applying is local. The row must
		// not offer to cross that gap.
		{"remote project active", &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}, "0.0.1", true, "Updates apply locally (remote project active)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aboutUpdateLabel(tc.info, tc.current, tc.remote); got != tc.want {
				t.Errorf("aboutUpdateLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHandleUpdateAction_RemoteProject_RefusesWithoutSending pins the matching
// refusal on the action side: staging would happen on the far host's disk and
// applying would swap this machine's binaries, so nothing may go on the wire.
func TestHandleUpdateAction_RemoteProject_RefusesWithoutSending(t *testing.T) {
	fake := &fakeSender{}
	m := Model{
		client:      fake,
		version:     "0.0.1",
		projects:    []*ProjectModel{{ID: "p1", Dest: "gpu01"}},
		updateInfos: map[string]*ipc.UpdateInfo{"gpu01": {LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}},
	}
	out, _ := m.handleUpdateAction()
	got := out.(Model)

	if len(fake.sent) != 0 {
		t.Errorf("sent = %d messages, want 0 (refused for a remote project)", len(fake.sent))
	}
	if got.dialog == dialogConfirm {
		t.Error("opened the apply confirm for a remote announcement, want refused")
	}
	if got.pendingApplyVer != "" {
		t.Errorf("pendingApplyVer = %q, want empty", got.pendingApplyVer)
	}
}

func TestAboutUpdateLabel_UpdatesDisabled(t *testing.T) {
	version.SetUpdatesEnabled(false)
	t.Cleanup(func() { version.SetUpdatesEnabled(true) })

	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}
	if got := aboutUpdateLabel(info, "0.0.1", false); got != "Updates disabled (dev build)" {
		t.Errorf("aboutUpdateLabel with updates disabled = %q, want %q", got, "Updates disabled (dev build)")
	}
}

func TestUpdateAvailable_UpdatesDisabled(t *testing.T) {
	version.SetUpdatesEnabled(false)
	t.Cleanup(func() { version.SetUpdatesEnabled(true) })

	info := &ipc.UpdateInfo{LatestVersion: "0.0.2"}
	if updateAvailable(info, "0.0.1") {
		t.Error("updateAvailable = true with updates disabled, want false")
	}
}

func TestMaybeShowUpdateNotice(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1", updateInfos: localUpdate(&ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true})}
	m.maybeShowUpdateNotice("")
	if m.dialog != dialogUpdateNotice {
		t.Fatalf("dialog = %v, want dialogUpdateNotice", m.dialog)
	}

	// Second call for the same version: already notified → no dialog.
	m2 := &Model{version: "0.0.1", updateInfos: localUpdate(&ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true})}
	m2.maybeShowUpdateNotice("")
	if m2.dialog == dialogUpdateNotice {
		t.Error("second notice for same version shown, want suppressed")
	}

	// A modal other than the disclaimer blocks the notice.
	m3 := &Model{version: "0.0.1", dialog: dialogPluginMigration, updateInfos: localUpdate(&ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true})}
	m3.maybeShowUpdateNotice("")
	if m3.dialog != dialogPluginMigration {
		t.Error("notice replaced migration dialog, want migration kept")
	}

	// The disclaimer yields to the notice (spec: update notice > disclaimer).
	m4 := &Model{version: "0.0.1", dialog: dialogDisclaimer, updateInfos: localUpdate(&ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true})}
	m4.maybeShowUpdateNotice("")
	if m4.dialog != dialogUpdateNotice {
		t.Error("notice did not replace disclaimer, want replaced")
	}

	// Up to date → no dialog.
	m5 := &Model{version: "0.0.2", updateInfos: localUpdate(&ipc.UpdateInfo{LatestVersion: "0.0.2"})}
	m5.maybeShowUpdateNotice("")
	if m5.dialog == dialogUpdateNotice {
		t.Error("notice shown when up to date")
	}
}

// TestOpenAboutDialog_AsksForAFreshCheck pins that opening About re-checks.
// The row's label is drawn from the last broadcast, and the daemon refreshes
// that once a day — so without this the row can spend up to 24 h describing a
// release that is no longer the newest.
func TestOpenAboutDialog_AsksForAFreshCheck(t *testing.T) {
	fake := &fakeSender{}
	m := Model{client: fake, version: "0.0.1"}

	out, _ := m.openAboutDialog()
	if got := out.(Model).dialog; got != dialogAbout {
		t.Errorf("dialog = %v, want dialogAbout", got)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1 (the check request)", len(fake.sent))
	}
	if fake.sent[0].Type != ipc.MsgUpdateCheckReq {
		t.Errorf("sent[0].Type = %q, want %q", fake.sent[0].Type, ipc.MsgUpdateCheckReq)
	}
}

// TestOpenAboutDialog_DevBuildSendsNothing keeps the check off the wire for
// builds whose update pipeline is compiled out — the row there reads "Updates
// disabled" and no amount of checking can change it.
func TestOpenAboutDialog_DevBuildSendsNothing(t *testing.T) {
	version.SetUpdatesEnabled(false)
	t.Cleanup(func() { version.SetUpdatesEnabled(true) })

	fake := &fakeSender{}
	m := Model{client: fake, version: "0.0.1"}
	if _, _ = m.openAboutDialog(); len(fake.sent) != 0 {
		t.Errorf("sent = %d messages, want 0 (updates disabled)", len(fake.sent))
	}
}

// TestApplyUpdateConfirm_RequiresExplicitY covers the accept key. This confirm
// can open by itself when a stage request answers — minutes after the press
// that started it, with the user's hands back on a pane — so Enter, the
// universal commit key, must not be able to quit the TUI and swap binaries.
func TestApplyUpdateConfirm_RequiresExplicitY(t *testing.T) {
	base := Model{
		dialog:      dialogConfirm,
		confirmKind: confirmKindApplyUpdate,
		confirmName: "0.0.2",
	}

	out, _ := base.handleConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := out.(Model); got.applyUpdateOnExit {
		t.Error("Enter armed the apply — want it refused (y only)")
	}

	out, _ = base.handleConfirmKey(tea.KeyPressMsg{Text: "y"})
	if got := out.(Model); !got.applyUpdateOnExit {
		t.Error("y did not arm the apply")
	}
}

// TestApplyUpdateConfirm_EscClearsTheDetailAndReturnsWhereThePressCameFrom
// covers both halves of the Esc route out. The detail line describes ONE answer
// from the daemon, so leaving it set would reappear on an unrelated later
// confirm; and this dialog can now open by itself minutes after the press, so
// Esc must not paint the About menu over a pane the user went back to working
// in.
func TestApplyUpdateConfirm_EscClearsTheDetailAndReturnsWhereThePressCameFrom(t *testing.T) {
	esc := tea.KeyPressMsg{Code: tea.KeyEscape}

	fromAbout := Model{
		dialog: dialogConfirm, confirmKind: confirmKindApplyUpdate, confirmName: "0.0.2",
		confirmDetail: "newest-release check: dial tcp: no route to host",
		// The press came from the About menu, so that is where Esc belongs.
		applyConfirmReturn: dialogAbout,
	}
	out, _ := fromAbout.handleConfirmKey(esc)
	got := out.(Model)
	if got.dialog != dialogAbout {
		t.Errorf("dialog = %v, want dialogAbout (the press came from there)", got.dialog)
	}
	if got.dialogCursor != aboutUpdateIndex {
		t.Errorf("dialogCursor = %d, want the update row (%d)", got.dialogCursor, aboutUpdateIndex)
	}
	if got.confirmDetail != "" {
		t.Errorf("confirmDetail = %q, want cleared on the way out", got.confirmDetail)
	}

	selfOpened := Model{
		dialog: dialogConfirm, confirmKind: confirmKindApplyUpdate, confirmName: "0.0.2",
		applyConfirmReturn: dialogNone, // arrived on its own, after a download
	}
	out, _ = selfOpened.handleConfirmKey(esc)
	if got := out.(Model).dialog; got != dialogNone {
		t.Errorf("dialog = %v, want dialogNone — Esc must return to the panes, not a menu never opened", got)
	}
}

// TestUpdate_StageUpdateRespOpensTheApplyConfirm drives the decision through
// Update rather than calling applyStageUpdateResp directly. Everything above
// tests what the handler decides; this tests that the message arm still
// REACHES it — a wiring regression is invisible to a direct-call test, and the
// arm is the only thing standing between the daemon's answer and the user.
func TestUpdate_StageUpdateRespOpensTheApplyConfirm(t *testing.T) {
	m := Model{client: &fakeSender{}, version: "0.0.1", pendingApplyVer: "0.0.2"}

	out, _ := m.Update(stageUpdateRespMsg{
		Resp: ipc.StageUpdateRespPayload{Success: true, Version: "0.0.3"},
	})
	got := out.(Model)

	if got.dialog != dialogConfirm || got.confirmKind != confirmKindApplyUpdate {
		t.Fatalf("dialog = %v, confirmKind = %q, want the apply confirm", got.dialog, got.confirmKind)
	}
	if got.confirmName != "0.0.3" {
		t.Errorf("confirmName = %q, want 0.0.3 (the version the daemon actually staged)", got.confirmName)
	}
}

// TestUpdate_F1AsksForAFreshCheck pins the other new wiring: the About dialog
// opens through openAboutDialog, so pressing F1 puts a check on the wire.
func TestUpdate_F1AsksForAFreshCheck(t *testing.T) {
	fake := &fakeSender{}
	m := Model{client: fake, version: "0.0.1", width: 80, height: 24}

	out, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	if got := out.(Model).dialog; got != dialogAbout {
		t.Fatalf("dialog = %v, want dialogAbout", got)
	}
	if len(fake.sent) != 1 || fake.sent[0].Type != ipc.MsgUpdateCheckReq {
		t.Errorf("sent = %+v, want exactly one %s", fake.sent, ipc.MsgUpdateCheckReq)
	}
}

// localUpdate builds the announcement table for a single LOCAL daemon — the
// shape every pre-multi-daemon fixture in this file describes.
func localUpdate(info *ipc.UpdateInfo) map[string]*ipc.UpdateInfo {
	return map[string]*ipc.UpdateInfo{"": info}
}

// TestNoteWorkspaceState_OnlyFirstBroadcastOpensNotice guards against the
// startup notice reopening on every mid-session WorkspaceStateMsg (switch
// tab, create pane, ...) — only the FIRST broadcast after attach may open it,
// even if a later broadcast announces a newer version.
func TestNoteWorkspaceState_OnlyFirstBroadcastOpensNotice(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1"}
	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}
	m.noteWorkspaceState(info, "")
	if m.dialog != dialogUpdateNotice {
		t.Fatalf("first broadcast: dialog = %v, want dialogUpdateNotice", m.dialog)
	}
	if !m.sawFirstState {
		t.Error("sawFirstState not set after first broadcast")
	}

	// User dismissed the notice; a later broadcast announces an even newer
	// version — it must NOT reopen the dialog.
	m.dialog = dialogNone
	newer := &ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true}
	m.noteWorkspaceState(newer, "")
	if m.dialog == dialogUpdateNotice {
		t.Error("second broadcast reopened the notice, want suppressed")
	}
	if m.updateInfoFor("") != newer {
		t.Error("update info not refreshed on second broadcast")
	}
}

// TestHandleUpdateAction covers every branch of the About/notice update
// action: updates-disabled short-circuits before any network send; an
// up-to-date report sends a re-check request (not just a flash); unwritable
// flashes without sending; a fully staged version ALSO sends a re-check
// (recording the apply intent rather than confirming straight from the
// broadcast); and a known-but-unstaged version sends the stage request.
func TestHandleUpdateAction(t *testing.T) {
	cases := []struct {
		name             string
		updatesOff       bool
		info             *ipc.UpdateInfo
		version          string
		wantSent         bool
		wantSentType     string
		wantDialog       dialogScreen
		wantConfirmKnd   string
		wantPendingApply string
	}{
		{
			name:       "updates disabled",
			updatesOff: true,
			info:       &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true},
			version:    "0.0.1",
			wantSent:   false,
			wantDialog: dialogNone,
		},
		{
			name:         "up to date sends recheck",
			info:         &ipc.UpdateInfo{LatestVersion: "0.0.1"},
			version:      "0.0.1",
			wantSent:     true,
			wantSentType: ipc.MsgStageUpdateReq,
			wantDialog:   dialogNone,
		},
		{
			name:       "unwritable flashes without sending",
			info:       &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: false},
			version:    "0.0.1",
			wantSent:   false,
			wantDialog: dialogNone,
		},
		{
			// The regression this whole change exists for: the broadcast
			// saying "0.0.2 is staged" is up to 24 h old, so pressing the row
			// must ask the daemon what is actually latest instead of
			// confirming an install of whatever was newest yesterday.
			name:             "staged re-checks instead of confirming",
			info:             &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true},
			version:          "0.0.1",
			wantSent:         true,
			wantSentType:     ipc.MsgStageUpdateReq,
			wantDialog:       dialogNone,
			wantPendingApply: "0.0.2",
		},
		{
			name:         "not staged sends stage request",
			info:         &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true},
			version:      "0.0.1",
			wantSent:     true,
			wantSentType: ipc.MsgStageUpdateReq,
			wantDialog:   dialogNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.updatesOff {
				version.SetUpdatesEnabled(false)
				t.Cleanup(func() { version.SetUpdatesEnabled(true) })
			}
			fake := &fakeSender{}
			m := Model{client: fake, version: tc.version, updateInfos: localUpdate(tc.info)}
			out, _ := m.handleUpdateAction()
			got := out.(Model)

			if tc.wantSent && len(fake.sent) != 1 {
				t.Fatalf("sent = %d messages, want 1", len(fake.sent))
			}
			if !tc.wantSent && len(fake.sent) != 0 {
				t.Fatalf("sent = %d messages, want 0", len(fake.sent))
			}
			if tc.wantSent && fake.sent[0].Type != tc.wantSentType {
				t.Errorf("sent[0].Type = %q, want %q", fake.sent[0].Type, tc.wantSentType)
			}
			if got.dialog != tc.wantDialog {
				t.Errorf("dialog = %v, want %v", got.dialog, tc.wantDialog)
			}
			if tc.wantConfirmKnd != "" && got.confirmKind != tc.wantConfirmKnd {
				t.Errorf("confirmKind = %q, want %q", got.confirmKind, tc.wantConfirmKnd)
			}
			if got.pendingApplyVer != tc.wantPendingApply {
				t.Errorf("pendingApplyVer = %q, want %q", got.pendingApplyVer, tc.wantPendingApply)
			}
		})
	}
}

// TestHandleUpdateAction_DownloadPressClearsAnEarlierApplyIntent pins that the
// intent flag cannot leak between presses. A staged press records "apply when
// this returns"; if the user escapes and later presses an ordinary download
// row, that press must NOT inherit the intent and pop an apply confirm the
// user never asked for.
func TestHandleUpdateAction_DownloadPressClearsAnEarlierApplyIntent(t *testing.T) {
	fake := &fakeSender{}
	m := Model{
		client:          fake,
		version:         "0.0.1",
		updateInfos:     localUpdate(&ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true}),
		pendingApplyVer: "0.0.2", // left over from an earlier staged press
	}
	out, _ := m.handleUpdateAction()
	if got := out.(Model).pendingApplyVer; got != "" {
		t.Errorf("pendingApplyVer = %q after a download press, want cleared", got)
	}
}

// TestHandleUpdateAction_SecondPressWhileInFlight_SendsNothing guards the
// regression that re-created this PR's own bug: press 1 starts a download of a
// newer release, press 2 (the row still reads "staged") gets answered from the
// daemon's staging CAS in milliseconds, and that fast failure used to open an
// apply confirm for the OLDER staged version — installing the intermediate
// release and abandoning the download of the newest.
func TestHandleUpdateAction_SecondPressWhileInFlight_SendsNothing(t *testing.T) {
	fake := &fakeSender{}
	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}
	m := Model{client: fake, version: "0.0.1", updateInfos: localUpdate(info)}

	out, _ := m.handleUpdateAction()
	first := out.(Model)
	if len(fake.sent) != 1 {
		t.Fatalf("first press sent %d messages, want 1", len(fake.sent))
	}
	if !first.updateReqInFlight {
		t.Fatal("first press did not mark the request in flight")
	}

	out, _ = first.handleUpdateAction()
	second := out.(Model)
	if len(fake.sent) != 1 {
		t.Errorf("second press sent another message (total %d), want it suppressed", len(fake.sent))
	}
	if second.dialog == dialogConfirm {
		t.Error("second press opened a confirm, want the press refused")
	}
	// The FIRST press's intent must survive: its response is still coming.
	if second.pendingApplyVer != "0.0.2" {
		t.Errorf("pendingApplyVer = %q, want the first press's intent preserved", second.pendingApplyVer)
	}
}

// TestApplyStageUpdateResp covers what the daemon's answer does next. The
// pendingApplyVer cases are the fix: a press meaning APPLY continues into
// the confirm for whatever the daemon actually has — the same stage when it is
// still latest, the NEWER one when it staged one just now — and falls back to
// the on-disk version when the check could not be made at all.
func TestApplyStageUpdateResp(t *testing.T) {
	cases := []struct {
		name        string
		pending     string
		openDialog  dialogScreen
		resp        ipc.StageUpdateRespPayload
		wantDialog  dialogScreen
		wantConfirm string
		wantDetail  bool
	}{
		{
			name:        "apply intent, stage still latest",
			pending:     "0.0.2",
			resp:        ipc.StageUpdateRespPayload{Success: true, AlreadyStaged: true, Version: "0.0.2"},
			wantDialog:  dialogConfirm,
			wantConfirm: "0.0.2",
		},
		{
			// The user pressed apply on a staged 0.0.2; the re-check found
			// 0.0.3 and staged it. The confirm must name 0.0.3 — installing
			// 0.0.2 here is exactly the intermediate-version loop being fixed.
			name:        "apply intent, newer release staged instead",
			pending:     "0.0.2",
			resp:        ipc.StageUpdateRespPayload{Success: true, Version: "0.0.3"},
			wantDialog:  dialogConfirm,
			wantConfirm: "0.0.3",
		},
		{
			name:        "apply intent, check failed — falls back to the staged version",
			pending:     "0.0.2",
			resp:        ipc.StageUpdateRespPayload{CheckFailed: true, Error: "check release: dial tcp: no route to host"},
			wantDialog:  dialogConfirm,
			wantConfirm: "0.0.2",
			wantDetail:  true,
		},
		{
			// The regression guard for the impatient second press. While press 1
			// downloads 0.0.3, press 2 is answered from the daemon's staging CAS
			// within milliseconds. Treating that as a failed CHECK would confirm
			// an install of 0.0.2 and abandon the 0.0.3 download — the
			// intermediate-version loop this change exists to end. Only
			// CheckFailed licenses the fallback.
			name:       "apply intent, stage already running — must NOT offer the older stage",
			pending:    "0.0.2",
			resp:       ipc.StageUpdateRespPayload{Error: "stage: staging already in progress"},
			wantDialog: dialogNone,
		},
		{
			name:       "apply intent, install dir not writable — no confirm",
			pending:    "0.0.2",
			resp:       ipc.StageUpdateRespPayload{Error: "install directory not writable"},
			wantDialog: dialogNone,
		},
		{
			// GitHub ANSWERED and said nothing is newer (the staged release was
			// yanked). The question is settled, so this is a flash — a confirm
			// here would read "Apply update v0.0.2?" over "already up to date".
			name:       "apply intent, up-to-date answer flashes rather than confirming",
			pending:    "0.0.2",
			resp:       ipc.StageUpdateRespPayload{Error: "already up to date"},
			wantDialog: dialogNone,
		},
		{
			// A stage takes as long as the download takes. If the user has
			// opened something else since, the confirm must not replace it.
			name:       "apply intent refuses to replace an open dialog",
			pending:    "0.0.2",
			openDialog: dialogSettings,
			resp:       ipc.StageUpdateRespPayload{Success: true, AlreadyStaged: true, Version: "0.0.2"},
			wantDialog: dialogSettings,
		},
		{
			name:       "download press stays flash-only",
			resp:       ipc.StageUpdateRespPayload{Success: true, Version: "0.0.2"},
			wantDialog: dialogNone,
		},
		{
			name:       "up to date with no apply intent stays flash-only",
			resp:       ipc.StageUpdateRespPayload{Error: "already up to date"},
			wantDialog: dialogNone,
		},
		{
			name:       "failure with no apply intent stays flash-only",
			resp:       ipc.StageUpdateRespPayload{Error: "install directory not writable"},
			wantDialog: dialogNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{version: "0.0.1", dialog: tc.openDialog, pendingApplyVer: tc.pending}
			got, _ := m.applyStageUpdateResp(tc.resp)

			if got.dialog != tc.wantDialog {
				t.Errorf("dialog = %v, want %v", got.dialog, tc.wantDialog)
			}
			if tc.wantConfirm != "" {
				if got.confirmKind != confirmKindApplyUpdate {
					t.Errorf("confirmKind = %q, want %q", got.confirmKind, confirmKindApplyUpdate)
				}
				if got.confirmName != tc.wantConfirm {
					t.Errorf("confirmName = %q, want %q", got.confirmName, tc.wantConfirm)
				}
			}
			if hasDetail := got.confirmDetail != ""; hasDetail != tc.wantDetail {
				t.Errorf("confirmDetail = %q, want detail: %v", got.confirmDetail, tc.wantDetail)
			}
			// The intent is one-shot: a second, unrelated response must never
			// re-open the confirm.
			if got.pendingApplyVer != "" {
				t.Errorf("pendingApplyVer = %q, want cleared after the response", got.pendingApplyVer)
			}
			// Every response ends the in-flight window, or the row refuses every
			// later press for the rest of the session.
			if got.updateReqInFlight {
				t.Error("updateReqInFlight still set after a response, want cleared")
			}
		})
	}
}
