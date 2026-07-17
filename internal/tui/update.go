package tui

import (
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
