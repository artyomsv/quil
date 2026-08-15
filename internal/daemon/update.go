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

// updateRecheckMinInterval rate-limits the client-driven check-only refresh
// (MsgUpdateCheckReq). Opening the About dialog is a frequent, cheap gesture
// and every attached client can make it, so the daemon — not the client — owns
// the bound: unauthenticated GitHub allows 60 req/hr/IP, and this caps the
// path at 12 of them however many TUIs are attached and however often F1 is
// pressed.
const updateRecheckMinInterval = 5 * time.Minute

// newUpdateChecker builds the release checker. A package var so tests can
// point the daemon at an httptest server; production never reassigns it.
var newUpdateChecker = func() *update.Checker { return &update.Checker{} }

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
			d.runUpdateCheck(d.cfg.Update.Auto)
			timer.Reset(updateCheckInterval)
		}
	}
}

// runUpdateCheck performs one check and refreshes the broadcast update info,
// staging the release when allowStage says so. The daily tick passes
// [update] auto; the client-driven refresh passes false — it exists to make a
// stale label truthful, and downloading 15 MB because someone opened a dialog
// is not what that asked for.
func (d *Daemon) runUpdateCheck(allowStage bool) {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()

	d.stampUpdateCheck()
	rel, err := newUpdateChecker().Latest(ctx)
	if err != nil {
		// Network failures are routine (offline laptop); keep quiet. The
		// attempt is already stamped above — pacing must not depend on the
		// answer, or a 403 from a spent quota would lift the rate limit.
		logger.Debug("update check: %v", err)
		return
	}

	now := time.Now().UnixMilli()
	if cmp, cmpErr := version.Compare(rel.Version(), version.Current()); cmpErr != nil || cmp <= 0 {
		// Up to date (or unparseable tag): clear any stale announcement.
		d.mutateUpdateState(func(s *update.State) {
			s.LastCheckMs, s.LatestVersion, s.ReleaseURL = now, rel.Version(), rel.URL
			s.StagedVersion = ""
		})
		if d.setUpdateInfo(nil) {
			d.broadcastState()
		}
		return
	}

	writable := installDirWritable()
	st := d.mutateUpdateState(func(s *update.State) {
		s.LastCheckMs, s.LatestVersion, s.ReleaseURL = now, rel.Version(), rel.URL
		s.InstallWritable = writable
	})

	// The download runs OUTSIDE the state lock, deliberately: it takes minutes,
	// and holding the lock across it would park every other writer — including
	// the on-demand path's recordStagedVersion — behind a network transfer.
	if allowStage && writable && st.StagedVersion != rel.Version() {
		if stageErr := d.stageRelease(ctx, rel); stageErr != nil {
			log.Printf("update: stage v%s: %v", rel.Version(), stageErr)
		} else {
			st = d.mutateUpdateState(func(s *update.State) { s.StagedVersion = rel.Version() })
			update.PruneStaged(config.UpdateDir(), rel.Version())
			log.Printf("update: staged v%s (applies on next quil launch)", rel.Version())
		}
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
// Its own single-flight slot, separate from updateStaging: the AlreadyStaged
// branch answers BEFORE stageRelease's CAS, so that guard no longer bounds
// this handler. Without one, N concurrent requests each cost a GitHub API call
// (the same endpoint updateRecheckMinInterval exists to protect), a full
// re-hash of both staged binaries, and a read-modify-write of state.json whose
// fixed temp path two writers would interleave — leaving JSON that LoadState
// silently reads as the zero State, i.e. the announcement dropped. Mashing
// Enter on the About row is enough to reach it.
func (d *Daemon) handleStageUpdateReq(conn *ipc.Conn, msg *ipc.Message) {
	if !d.updateOnDemand.CompareAndSwap(false, true) {
		// Answered, not dropped: the client may be holding an apply intent, and
		// a request that never answers strands it. Deliberately NOT phrased as
		// a check failure — the client must not treat this as grounds to fall
		// back to installing an older stage (see StageUpdateRespPayload).
		respondTo(conn, msg.ID, ipc.MsgStageUpdateResp, ipc.StageUpdateRespPayload{
			Error: "an update check is already running",
		})
		return
	}
	go func() {
		defer d.updateOnDemand.Store(false)
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

	d.stampUpdateCheck()
	rel, err := newUpdateChecker().Latest(ctx)
	if err != nil {
		// CheckFailed is the whole reason the client may fall back to installing
		// what is already on disk: GitHub was unreachable, so "is this still the
		// newest?" is unanswered rather than answered no.
		return ipc.StageUpdateRespPayload{CheckFailed: true, Error: fmt.Sprintf("check release: %v", err)}
	}
	if err := update.SafeVersion(rel.Version()); err != nil {
		// Stage() rejects an unsafe tag before any network I/O, but the
		// AlreadyStaged branch below never reaches Stage — and this string is
		// persisted into state.json and broadcast as display text either way.
		return ipc.StageUpdateRespPayload{Error: err.Error()}
	}
	cmp, err := version.Compare(rel.Version(), version.Current())
	if err != nil || cmp <= 0 {
		return ipc.StageUpdateRespPayload{Error: "already up to date"}
	}

	// The just-checked release may already be on disk — the client re-sends
	// this request on every press of the update row, including the press that
	// means "apply what is staged". Answer from disk rather than re-downloading
	// an identical archive.
	//
	// The check is the MANIFEST plus a full re-hash, not the manifest alone:
	// a stage whose bytes no longer verify is one the apply path discards on
	// sight, so vouching for it here would report "ready" for an update that
	// then silently does not happen. A failed verify falls through and
	// re-stages, which is the repair.
	if man, dir, findErr := update.FindStaged(config.UpdateDir()); findErr == nil && man != nil &&
		man.Version == rel.Version() {
		vErr := update.VerifyStaged(dir, man, update.BinaryNames(runtime.GOOS))
		if vErr == nil {
			// Probed, not assumed: this branch never passed the writability
			// gate below, and the row it feeds says "applies on restart".
			d.recordStagedVersion(rel, installDirWritable())
			log.Printf("update: v%s was already staged — nothing downloaded", rel.Version())
			return ipc.StageUpdateRespPayload{Success: true, AlreadyStaged: true, Version: rel.Version()}
		}
		log.Printf("update: staged v%s failed verification (%v) — re-staging", man.Version, vErr)
	}

	if !installDirWritable() {
		return ipc.StageUpdateRespPayload{Error: "install directory not writable"}
	}
	if err := d.stageRelease(ctx, rel); err != nil {
		return ipc.StageUpdateRespPayload{Error: fmt.Sprintf("stage: %v", err)}
	}

	d.recordStagedVersion(rel, true) // installDirWritable() passed just above
	log.Printf("update: staged v%s on demand", rel.Version())
	return ipc.StageUpdateRespPayload{Success: true, Version: rel.Version()}
}

// recordStagedVersion persists "rel is the latest AND it is staged", prunes
// every older staging dir, and refreshes the broadcast info. Shared by the
// two stageOnDemand outcomes — a fresh download and a stage already on disk
// leave identical state, and the second must still write it: it is reached
// precisely when the persisted LatestVersion was stale.
// writable is a PARAMETER rather than a hardcoded true: the fresh-stage caller
// has just passed the installDirWritable() gate, but the already-staged one
// returns before it and never probes. Asserting true there promises "applies on
// restart" for an install dir the swap cannot write — the same class of wrong
// promise the full re-hash exists to prevent, and it persists into state.json
// where seedUpdateInfoFromState re-announces it across a restart.
func (d *Daemon) recordStagedVersion(rel *update.Release, writable bool) {
	st := d.mutateUpdateState(func(s *update.State) {
		s.LastCheckMs = time.Now().UnixMilli()
		s.LatestVersion = rel.Version()
		s.ReleaseURL = rel.URL
		s.StagedVersion = rel.Version()
		s.InstallWritable = writable
	})
	update.PruneStaged(config.UpdateDir(), rel.Version())
	d.setUpdateInfo(&ipc.UpdateInfo{
		LatestVersion:   st.LatestVersion,
		ReleaseURL:      st.ReleaseURL,
		StagedVersion:   st.StagedVersion,
		InstallWritable: st.InstallWritable,
	})
}

// mutateUpdateState is the ONE read-modify-write path for state.json, taken
// under updateStateMu and returning the state it wrote.
//
// Three callers can now overlap — the daily tick, the check-only refresh, and
// the on-demand stage — and the file is a load-modify-save with no atomicity of
// its own. Worse, update.saveJSON writes through a FIXED "<path>.tmp": two
// concurrent savers open it O_TRUNC on separate descriptors, so a shorter write
// leaves the longer one's tail behind and the renamed file can be invalid JSON,
// which LoadState reports as the zero State — silently dropping the staged
// announcement. Serialising here restores the "single writer per file" the
// pipeline is designed around. Never hold this across a download.
func (d *Daemon) mutateUpdateState(fn func(*update.State)) update.State {
	d.updateStateMu.Lock()
	defer d.updateStateMu.Unlock()
	st := update.LoadState(config.UpdateStatePath())
	fn(&st)
	if err := update.SaveState(config.UpdateStatePath(), st); err != nil {
		log.Printf("update: save state: %v", err)
	}
	return st
}

// stampUpdateCheck records that a release check was ATTEMPTED, in memory.
//
// The persisted LastCheckMs cannot carry this: runUpdateCheck returns before
// SaveState whenever the check errors, so a failing check never advances it —
// and the failure that matters is GitHub answering 403 because the very quota
// this limiter protects is spent. The persisted value would then stay frozen
// and every About-dialog open would issue another request, i.e. the limiter
// switches off exactly when it is needed. Stamping the attempt also keeps the
// dispatch goroutine free of the state.json read the check used to do there.
func (d *Daemon) stampUpdateCheck() {
	d.lastUpdateCheckAt.Store(time.Now().UnixMilli())
}

// updateCheckedRecently reports whether a check was attempted inside the
// rate-limit window. A negative age (state written under a clock later
// corrected backwards, or a future timestamp on disk) counts as NOT recent —
// treating it as recent would disable checking until real time caught up.
func (d *Daemon) updateCheckedRecently() bool {
	last := d.lastUpdateCheckAt.Load()
	if last == 0 {
		return false
	}
	since := time.Since(time.UnixMilli(last))
	return since >= 0 && since < updateRecheckMinInterval
}

// handleUpdateCheckReq refreshes the announced release without downloading
// anything — the About dialog fires it on open so its update row describes
// GitHub now rather than whenever the daily tick last ran.
//
// Fire-and-forget by design: there is no response type. The answer is the
// refreshed "update" key on the broadcast that runUpdateCheck emits when
// something changed, and a failed check (offline laptop) is a routine
// non-event that must not surface as a dialog error.
func (d *Daemon) handleUpdateCheckReq() {
	if !d.cfg.Update.Check || !version.IsRelease() || !version.UpdatesEnabled() {
		return
	}
	// Rate limit BEFORE the CAS, and from memory: this runs on the conn's
	// dispatch goroutine, where the daemon does no filesystem work at all (a
	// parked syscall freezes that client's whole dispatch loop, and QUIL_HOME
	// can sit on an unresponsive mount).
	if d.updateCheckedRecently() {
		logger.Debug("update: check-only request within %s of the last check — skipped", updateRecheckMinInterval)
		return
	}
	if !d.updateChecking.CompareAndSwap(false, true) {
		return // a check is already in flight; its result is the answer
	}
	d.stampUpdateCheck()
	go func() {
		defer d.updateChecking.Store(false)
		d.runUpdateCheck(false)
	}()
}
