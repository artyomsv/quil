package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
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
