package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/update"
	"github.com/artyomsv/quil/internal/version"
)

// updateCheckInitialDelay defers the first release check past daemon
// startup so a heavy workspace restore is never slowed by network I/O.
const updateCheckInitialDelay = 1 * time.Minute

// updateCheckInterval paces the recurring release check. Unauthenticated
// GitHub API allows 60 req/hr/IP; one per day is far below it.
const updateCheckInterval = 24 * time.Hour

// updateCheckTimeout bounds one full check+stage cycle (JSON GET + archive
// download + extraction).
const updateCheckTimeout = 10 * time.Minute

// seedUpdateInfoFromState re-announces a previously-detected update
// immediately after daemon restart (the daily tick would otherwise leave a
// 1-day blind spot). Only announces when updates are enabled for this build,
// checking is turned on, and the persisted latest is still newer than this
// (possibly just-upgraded) daemon. A StagedVersion claim in state.json is
// cross-checked against the actual staged manifest on disk — the staged dir
// can be pruned or deleted between runs, and the TUI must not show "ready"
// for a phantom stage.
func (d *Daemon) seedUpdateInfoFromState() {
	if !version.IsRelease() || !d.cfg.Update.Check || !version.UpdatesEnabled() {
		return
	}
	st := update.LoadState(config.UpdateStatePath())
	if st.LatestVersion == "" {
		return
	}
	cmp, err := version.Compare(st.LatestVersion, version.Current())
	if err != nil || cmp <= 0 {
		return
	}
	if st.StagedVersion != "" {
		man, _, findErr := update.FindStaged(config.UpdateDir())
		if findErr != nil || man == nil || man.Version != st.StagedVersion {
			st.StagedVersion = ""
		}
	}
	d.setUpdateInfo(&ipc.UpdateInfo{
		LatestVersion:   st.LatestVersion,
		ReleaseURL:      st.ReleaseURL,
		StagedVersion:   st.StagedVersion,
		InstallWritable: st.InstallWritable,
	})
}

// updateChecker is the daily release check + auto-stage loop. Started from
// Start() alongside idleChecker; exits on d.shutdown. Entirely inert for
// dev builds and when [update] check = false.
func (d *Daemon) updateChecker() {
	if !d.cfg.Update.Check || !version.IsRelease() || !version.UpdatesEnabled() {
		return
	}
	timer := time.NewTimer(updateCheckInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-d.shutdown:
			return
		case <-timer.C:
			d.runUpdateCheck()
			timer.Reset(updateCheckInterval)
		}
	}
}

// runUpdateCheck performs one check (and, when enabled and possible, one
// stage) and refreshes the broadcast update info.
func (d *Daemon) runUpdateCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()

	checker := &update.Checker{}
	rel, err := checker.Latest(ctx)
	if err != nil {
		// Network failures are routine (offline laptop); keep quiet.
		logger.Debug("update check: %v", err)
		return
	}

	st := update.LoadState(config.UpdateStatePath())
	st.LastCheckMs = time.Now().UnixMilli()
	st.LatestVersion = rel.Version()
	st.ReleaseURL = rel.URL

	cmp, err := version.Compare(rel.Version(), version.Current())
	if err != nil || cmp <= 0 {
		// Up to date (or unparseable tag): clear any stale announcement.
		st.StagedVersion = ""
		if saveErr := update.SaveState(config.UpdateStatePath(), st); saveErr != nil {
			log.Printf("update: save state: %v", saveErr)
		}
		if d.setUpdateInfo(nil) {
			d.broadcastState()
		}
		return
	}

	st.InstallWritable = installDirWritable()
	if d.cfg.Update.Auto && st.InstallWritable && st.StagedVersion != rel.Version() {
		if stageErr := d.stageRelease(ctx, rel); stageErr != nil {
			log.Printf("update: stage v%s: %v", rel.Version(), stageErr)
		} else {
			st.StagedVersion = rel.Version()
			update.PruneStaged(config.UpdateDir(), rel.Version())
			log.Printf("update: staged v%s (applies on next quil launch)", rel.Version())
		}
	}
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		log.Printf("update: save state: %v", err)
	}

	info := &ipc.UpdateInfo{
		LatestVersion:   st.LatestVersion,
		ReleaseURL:      st.ReleaseURL,
		StagedVersion:   st.StagedVersion,
		InstallWritable: st.InstallWritable,
	}
	if d.setUpdateInfo(info) {
		d.broadcastState()
	}
}

// stageRelease runs the download/verify/extract pipeline, single-flight
// guarded so the daily tick and an on-demand request can't stage twice
// concurrently.
func (d *Daemon) stageRelease(ctx context.Context, rel *update.Release) error {
	if !d.updateStaging.CompareAndSwap(false, true) {
		return fmt.Errorf("staging already in progress")
	}
	defer d.updateStaging.Store(false)
	s := &update.Stager{Root: config.UpdateDir(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	return s.Stage(ctx, rel)
}

// installDirWritable probes the daemon executable's own directory — the
// swap target of the apply step.
func installDirWritable() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return update.InstallWritable(filepath.Dir(exe))
}

// setUpdateInfo swaps the broadcast update info, reporting whether it
// changed (callers broadcast only on change). nil clears it.
func (d *Daemon) setUpdateInfo(info *ipc.UpdateInfo) bool {
	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	old := d.updateInfo
	same := (old == nil && info == nil) ||
		(old != nil && info != nil && *old == *info)
	d.updateInfo = info
	return !same
}

// currentUpdateInfo returns a copy-safe pointer for the state broadcast.
func (d *Daemon) currentUpdateInfo() *ipc.UpdateInfo {
	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	return d.updateInfo
}

// handleStageUpdateReq stages the latest release on demand (About → Update
// now before the daily tick staged anything). The download takes seconds
// to minutes, so it must NOT run on the conn's dispatch goroutine — the
// worker responds when done and refreshes the broadcast on success.
func (d *Daemon) handleStageUpdateReq(conn *ipc.Conn, msg *ipc.Message) {
	go func() {
		payload := d.stageOnDemand()
		respondTo(conn, msg.ID, ipc.MsgStageUpdateResp, payload)
		if payload.Success {
			d.broadcastState()
		}
	}()
}

func (d *Daemon) stageOnDemand() ipc.StageUpdateRespPayload {
	if !version.IsRelease() || !version.UpdatesEnabled() {
		return ipc.StageUpdateRespPayload{Error: "dev build — updates disabled"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()

	rel, err := (&update.Checker{}).Latest(ctx)
	if err != nil {
		return ipc.StageUpdateRespPayload{Error: fmt.Sprintf("check release: %v", err)}
	}
	cmp, err := version.Compare(rel.Version(), version.Current())
	if err != nil || cmp <= 0 {
		return ipc.StageUpdateRespPayload{Error: "already up to date"}
	}
	if !installDirWritable() {
		return ipc.StageUpdateRespPayload{Error: "install directory not writable"}
	}
	if err := d.stageRelease(ctx, rel); err != nil {
		return ipc.StageUpdateRespPayload{Error: fmt.Sprintf("stage: %v", err)}
	}

	st := update.LoadState(config.UpdateStatePath())
	st.LastCheckMs = time.Now().UnixMilli()
	st.LatestVersion = rel.Version()
	st.ReleaseURL = rel.URL
	st.StagedVersion = rel.Version()
	st.InstallWritable = true
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		log.Printf("update: save state: %v", err)
	}
	update.PruneStaged(config.UpdateDir(), rel.Version())
	d.setUpdateInfo(&ipc.UpdateInfo{
		LatestVersion:   st.LatestVersion,
		ReleaseURL:      st.ReleaseURL,
		StagedVersion:   st.StagedVersion,
		InstallWritable: true,
	})
	log.Printf("update: staged v%s on demand", rel.Version())
	return ipc.StageUpdateRespPayload{Success: true, Version: rel.Version()}
}
