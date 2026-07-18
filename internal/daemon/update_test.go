package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/update"
	"github.com/artyomsv/quil/internal/version"
)

// TestBuildWorkspaceState_UpdateKey asserts the broadcast state carries the
// "update" key exactly when update info is set.
func TestBuildWorkspaceState_UpdateKey(t *testing.T) {
	d := New(config.Default())

	state := d.buildWorkspaceState()
	if _, ok := state["update"]; ok {
		t.Error("update key present with no update info")
	}

	info := &ipc.UpdateInfo{LatestVersion: "0.0.2", StagedVersion: "0.0.2", InstallWritable: true}
	if changed := d.setUpdateInfo(info); !changed {
		t.Error("setUpdateInfo(first) = false, want true (changed)")
	}
	if changed := d.setUpdateInfo(info); changed {
		t.Error("setUpdateInfo(same) = true, want false (unchanged)")
	}

	state = d.buildWorkspaceState()
	got, ok := state["update"].(*ipc.UpdateInfo)
	if !ok {
		t.Fatalf("state[update] = %T, want *ipc.UpdateInfo", state["update"])
	}
	if got.LatestVersion != "0.0.2" || got.StagedVersion != "0.0.2" || !got.InstallWritable {
		t.Errorf("state[update] = %+v", got)
	}

	if changed := d.setUpdateInfo(nil); !changed {
		t.Error("setUpdateInfo(nil after set) = false, want true")
	}
	if _, ok := d.buildWorkspaceState()["update"]; ok {
		t.Error("update key present after clearing info")
	}
}

// withVersionState sets version.Current/UpdatesEnabled for the duration of
// a test and restores the prior globals on cleanup (they are process-wide).
func withVersionState(t *testing.T, current string, updatesEnabled bool) {
	t.Helper()
	origCurrent := version.Current()
	origEnabled := version.UpdatesEnabled()
	t.Cleanup(func() {
		version.SetCurrent(origCurrent)
		version.SetUpdatesEnabled(origEnabled)
	})
	version.SetCurrent(current)
	version.SetUpdatesEnabled(updatesEnabled)
}

// TestSeedUpdateInfoFromState_StagedVersionMissingOnDisk_ClearsAnnouncement
// covers the case where state.json claims a version is staged but the
// staged dir was pruned/deleted between daemon runs — the seed must
// announce the newer version without a stale "ready" claim.
func TestSeedUpdateInfoFromState_StagedVersionMissingOnDisk_ClearsAnnouncement(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)

	st := update.State{
		LatestVersion:   "1.1.0",
		ReleaseURL:      "https://example.invalid/r",
		StagedVersion:   "1.1.0", // claimed staged; nothing written to disk
		InstallWritable: true,
	}
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	cfg := config.Default()
	cfg.Update.Check = true
	d := New(cfg)
	d.seedUpdateInfoFromState()

	info := d.currentUpdateInfo()
	if info == nil {
		t.Fatal("currentUpdateInfo() = nil, want an announced update")
	}
	if info.LatestVersion != "1.1.0" {
		t.Errorf("LatestVersion = %q, want 1.1.0", info.LatestVersion)
	}
	if info.StagedVersion != "" {
		t.Errorf("StagedVersion = %q, want empty (phantom stage cleared)", info.StagedVersion)
	}
}

// TestSeedUpdateInfoFromState_StagedVersionOnDisk_Preserved is the happy-path
// counterpart: when the manifest on disk matches state.json's claim, the
// StagedVersion survives the seed.
func TestSeedUpdateInfoFromState_StagedVersionOnDisk_Preserved(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)

	st := update.State{
		LatestVersion:   "1.1.0",
		StagedVersion:   "1.1.0",
		InstallWritable: true,
	}
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	stagedDir := filepath.Join(config.UpdateDir(), "staged", "1.1.0")
	if err := os.MkdirAll(stagedDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	man := update.Manifest{Version: "1.1.0", Files: map[string]string{}, StagedAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(man)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedDir, "manifest.json"), data, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := config.Default()
	cfg.Update.Check = true
	d := New(cfg)
	d.seedUpdateInfoFromState()

	info := d.currentUpdateInfo()
	if info == nil {
		t.Fatal("currentUpdateInfo() = nil, want an announced update")
	}
	if info.StagedVersion != "1.1.0" {
		t.Errorf("StagedVersion = %q, want 1.1.0 (manifest on disk matches)", info.StagedVersion)
	}
}

// TestSeedUpdateInfoFromState_UpdatesDisabled_NoAnnounce covers dev/debug
// builds (version.UpdatesEnabled() == false): even with a newer version
// persisted in state.json, nothing gets announced.
func TestSeedUpdateInfoFromState_UpdatesDisabled_NoAnnounce(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", false)

	st := update.State{LatestVersion: "1.1.0", InstallWritable: true}
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	cfg := config.Default()
	cfg.Update.Check = true
	d := New(cfg)
	d.seedUpdateInfoFromState()

	if info := d.currentUpdateInfo(); info != nil {
		t.Errorf("currentUpdateInfo() = %+v, want nil (updates disabled)", info)
	}
}
