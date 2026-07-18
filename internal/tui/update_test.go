package tui

import (
	"testing"

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
		want    string
	}{
		{"up to date", nil, "0.0.1", "Check for updates (up to date)"},
		{"staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}, "0.0.1", "Update to v0.0.2 (staged — applies on restart)"},
		{"not staged", &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}, "0.0.1", "Update to v0.0.2 (download)"},
		{"unwritable", &ipc.UpdateInfo{LatestVersion: "0.0.2"}, "0.0.1", "Update available: v0.0.2 (manual install)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aboutUpdateLabel(tc.info, tc.current); got != tc.want {
				t.Errorf("aboutUpdateLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAboutUpdateLabel_UpdatesDisabled(t *testing.T) {
	version.SetUpdatesEnabled(false)
	t.Cleanup(func() { version.SetUpdatesEnabled(true) })

	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}
	if got := aboutUpdateLabel(info, "0.0.1"); got != "Updates disabled (dev build)" {
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

	m := &Model{version: "0.0.1", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}}
	m.maybeShowUpdateNotice()
	if m.dialog != dialogUpdateNotice {
		t.Fatalf("dialog = %v, want dialogUpdateNotice", m.dialog)
	}

	// Second call for the same version: already notified → no dialog.
	m2 := &Model{version: "0.0.1", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}}
	m2.maybeShowUpdateNotice()
	if m2.dialog == dialogUpdateNotice {
		t.Error("second notice for same version shown, want suppressed")
	}

	// A modal other than the disclaimer blocks the notice.
	m3 := &Model{version: "0.0.1", dialog: dialogPluginMigration, updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true}}
	m3.maybeShowUpdateNotice()
	if m3.dialog != dialogPluginMigration {
		t.Error("notice replaced migration dialog, want migration kept")
	}

	// The disclaimer yields to the notice (spec: update notice > disclaimer).
	m4 := &Model{version: "0.0.1", dialog: dialogDisclaimer, updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.3", InstallWritable: true}}
	m4.maybeShowUpdateNotice()
	if m4.dialog != dialogUpdateNotice {
		t.Error("notice did not replace disclaimer, want replaced")
	}

	// Up to date → no dialog.
	m5 := &Model{version: "0.0.2", updateInfo: &ipc.UpdateInfo{LatestVersion: "0.0.2"}}
	m5.maybeShowUpdateNotice()
	if m5.dialog == dialogUpdateNotice {
		t.Error("notice shown when up to date")
	}
}

// TestNoteWorkspaceState_OnlyFirstBroadcastOpensNotice guards against the
// startup notice reopening on every mid-session WorkspaceStateMsg (switch
// tab, create pane, ...) — only the FIRST broadcast after attach may open it,
// even if a later broadcast announces a newer version.
func TestNoteWorkspaceState_OnlyFirstBroadcastOpensNotice(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	m := &Model{version: "0.0.1"}
	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", InstallWritable: true}
	m.noteWorkspaceState(info)
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
	m.noteWorkspaceState(newer)
	if m.dialog == dialogUpdateNotice {
		t.Error("second broadcast reopened the notice, want suppressed")
	}
	if m.updateInfo != newer {
		t.Error("updateInfo not refreshed on second broadcast")
	}
}

// TestHandleUpdateAction covers every branch of the About/notice update
// action: updates-disabled short-circuits before any network send; an
// up-to-date report now sends a re-check request (not just a flash);
// unwritable flashes without sending; a fully staged version opens the
// apply confirm; and a known-but-unstaged version sends the stage request.
func TestHandleUpdateAction(t *testing.T) {
	cases := []struct {
		name           string
		updatesOff     bool
		info           *ipc.UpdateInfo
		version        string
		wantSent       bool
		wantSentType   string
		wantDialog     dialogScreen
		wantConfirmKnd string
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
			name:           "staged opens apply confirm",
			info:           &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true},
			version:        "0.0.1",
			wantSent:       false,
			wantDialog:     dialogConfirm,
			wantConfirmKnd: confirmKindApplyUpdate,
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
			m := Model{client: fake, version: tc.version, updateInfo: tc.info}
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
		})
	}
}
