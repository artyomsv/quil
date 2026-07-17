package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
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
