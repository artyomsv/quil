package daemon

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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

// releaseStub counts what a test's code path actually asked the "GitHub" for.
// Both counters are atomic: they are written on httptest's goroutines and read
// on the test's, and the check-only path answers from a worker.
type releaseStub struct {
	releaseHits *atomic.Int64 // GETs of the release JSON — i.e. checks
	archiveHits *atomic.Int64 // GETs of the archive — i.e. downloads
}

// stubDownloadableRelease serves a release whose archive can REALLY be
// downloaded, checksum-verified and extracted — the release JSON, the platform
// archive and checksums.txt all from one httptest server.
//
// A stub with no assets cannot express the cases that matter. Any path reaching
// Stage() fails there for want of an asset, so "a newer release superseded the
// staged one" becomes indistinguishable from "the supersede logic was deleted",
// and — subtler — a *download* makes no HTTP request at all, so counting
// requests proves nothing about whether one was attempted. Serving the archive
// for real is what turns archiveHits into a falsifiable assertion. The archive
// is built for runtime.GOOS because stageOnDemand stages for the machine it
// runs on.
func stubDownloadableRelease(t *testing.T, ver string) *releaseStub {
	t.Helper()
	stub := &releaseStub{releaseHits: &atomic.Int64{}, archiveHits: &atomic.Int64{}}
	files := map[string][]byte{}
	for _, name := range update.BinaryNames(runtime.GOOS) {
		files[name] = []byte("fake " + name + " " + ver)
	}
	archive := buildReleaseArchive(t, files)
	sum := sha256.Sum256(archive)
	assetName := update.AssetName(ver, runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) {
		stub.archiveHits.Add(1)
		w.Write(archive)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), assetName)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		stub.releaseHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":"v%s","html_url":"https://example.invalid/r","assets":[
			{"name":%q,"browser_download_url":%q},
			{"name":"checksums.txt","browser_download_url":%q}]}`,
			ver, assetName, srv.URL+"/archive", srv.URL+"/sums")
	})

	orig := newUpdateChecker
	newUpdateChecker = func() *update.Checker { return &update.Checker{BaseURL: srv.URL} }
	t.Cleanup(func() { newUpdateChecker = orig })
	return stub
}

// buildReleaseArchive produces the archive format this platform's release
// ships: zip on Windows, gzip'd tar everywhere else.
func buildReleaseArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for name, data := range files {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatalf("zip create %s: %v", name, err)
			}
			if _, err := w.Write(data); err != nil {
				t.Fatalf("zip write %s: %v", name, err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0755, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// writeStagedManifest stages ver on disk for real: two binaries plus a
// manifest carrying their true hashes, so VerifyStaged passes.
func writeStagedManifest(t *testing.T, ver string) {
	t.Helper()
	dir := filepath.Join(config.UpdateDir(), "staged", ver)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	files := map[string]string{}
	for _, name := range update.BinaryNames(runtime.GOOS) {
		body := []byte("staged " + name + " " + ver)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0700); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		sum := sha256.Sum256(body)
		files[name] = hex.EncodeToString(sum[:])
	}
	man := update.Manifest{Version: ver, Files: files, StagedAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(man)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestStageOnDemand_LatestAlreadyStaged_AnswersWithoutDownloading is the
// daemon half of the stale-stage fix. The TUI now re-sends this request on
// every press of the update row — including the press that means "apply what
// is staged" — so the handler has to recognise a stage that is still current
// and say so, instead of spending another full archive download to arrive at
// the same bytes.
func TestStageOnDemand_LatestAlreadyStaged_AnswersWithoutDownloading(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)
	// A DOWNLOADABLE release, deliberately: against an assetless stub a skipped
	// shortcut makes no HTTP request either, so archiveHits would read 0 whether
	// the download was avoided or merely impossible.
	stub := stubDownloadableRelease(t, "1.1.0")
	writeStagedManifest(t, "1.1.0")

	d := New(config.Default())
	resp := d.stageOnDemand()

	if !resp.Success || !resp.AlreadyStaged {
		t.Fatalf("resp = %+v, want Success+AlreadyStaged (nothing to download)", resp)
	}
	if resp.Version != "1.1.0" {
		t.Errorf("Version = %q, want 1.1.0", resp.Version)
	}
	if got := stub.releaseHits.Load(); got != 1 {
		t.Errorf("release endpoint hit %d times, want exactly 1 (the check)", got)
	}
	if got := stub.archiveHits.Load(); got != 0 {
		t.Errorf("archive downloaded %d times, want 0 — the stage was already current", got)
	}
	// The point of answering at all is to correct the persisted state that was
	// stale enough to send the client here.
	st := update.LoadState(config.UpdateStatePath())
	if st.LatestVersion != "1.1.0" || st.StagedVersion != "1.1.0" {
		t.Errorf("state = %+v, want latest and staged both 1.1.0", st)
	}
	if info := d.currentUpdateInfo(); info == nil || info.StagedVersion != "1.1.0" {
		t.Errorf("currentUpdateInfo() = %+v, want the staged version announced", info)
	}
}

// TestStageOnDemand_NewerReleaseSupersedesTheStagedOne is THE test for the bug
// this whole change exists to fix, at the layer that decides it.
//
// v1.1.0 is staged on disk and GitHub now serves v1.2.0. The old code never
// asked, so the only thing the user could install was v1.1.0 — apply, restart,
// check again, apply again. Here the daemon must fetch v1.2.0, put its bytes on
// disk, prune v1.1.0, and NAME v1.2.0 in both the response and the broadcast.
//
// It runs against a really-downloadable archive: with the assetless stub every
// assertion below would pass on code that simply failed to download anything.
func TestStageOnDemand_NewerReleaseSupersedesTheStagedOne(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)
	writeStagedManifest(t, "1.1.0") // the stale stage the user is trapped on
	stubDownloadableRelease(t, "1.2.0")

	d := New(config.Default())
	resp := d.stageOnDemand()

	if !resp.Success {
		t.Fatalf("resp = %+v, want a successful stage of the newer release", resp)
	}
	if resp.AlreadyStaged {
		t.Error("AlreadyStaged = true, want false — 1.2.0 was not on disk")
	}
	if resp.Version != "1.2.0" {
		t.Errorf("Version = %q, want 1.2.0 (the newest), not the staged 1.1.0", resp.Version)
	}

	// The bytes, not just the bookkeeping: this is what the apply installs.
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err != nil || man == nil {
		t.Fatalf("FindStaged = (%+v, %q, %v), want the newly staged release", man, dir, err)
	}
	if man.Version != "1.2.0" {
		t.Errorf("staged manifest version = %q, want 1.2.0", man.Version)
	}
	if vErr := update.VerifyStaged(dir, man, update.BinaryNames(runtime.GOOS)); vErr != nil {
		t.Errorf("VerifyStaged on the fresh stage: %v", vErr)
	}
	if _, statErr := os.Stat(filepath.Join(config.UpdateDir(), "staged", "1.1.0")); !os.IsNotExist(statErr) {
		t.Errorf("staged/1.1.0 still present (stat err = %v), want pruned", statErr)
	}
	if info := d.currentUpdateInfo(); info == nil || info.StagedVersion != "1.2.0" || info.LatestVersion != "1.2.0" {
		t.Errorf("currentUpdateInfo() = %+v, want 1.2.0 announced as latest and staged", info)
	}
}

// TestStageOnDemand_CorruptStageIsRepaired is the other half of the disk check:
// a stage whose bytes no longer match its manifest must not be reported ready,
// and the repair is to re-stage it — which only a really-downloadable release
// can demonstrate. With an assetless stub the re-stage fails for the wrong
// reason and the test proves nothing about the repair.
func TestStageOnDemand_CorruptStageIsRepaired(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)
	writeStagedManifest(t, "1.2.0")
	stubDownloadableRelease(t, "1.2.0") // SAME version as the corrupt stage

	staged := filepath.Join(config.UpdateDir(), "staged", "1.2.0", update.BinaryNames(runtime.GOOS)[0])
	if err := os.WriteFile(staged, []byte("tampered"), 0700); err != nil {
		t.Fatalf("corrupt staged binary: %v", err)
	}

	d := New(config.Default())
	resp := d.stageOnDemand()

	if !resp.Success {
		t.Fatalf("resp = %+v, want the corrupt stage re-staged successfully", resp)
	}
	if resp.AlreadyStaged {
		t.Error("AlreadyStaged = true for a stage that failed verification, want a real re-stage")
	}
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err != nil || man == nil {
		t.Fatalf("FindStaged after repair = (%+v, %v)", man, err)
	}
	if vErr := update.VerifyStaged(dir, man, update.BinaryNames(runtime.GOOS)); vErr != nil {
		t.Errorf("VerifyStaged after repair: %v — the tampered bytes were not replaced", vErr)
	}
}

// TestRunUpdateCheck_AutoStagesANewerRelease covers the allowStage=true side of
// the parameter this change introduced — the DAILY tick with [update] auto on,
// which is how most installs actually receive an update.
//
// The on-demand path has the equivalent test above; this one exists because
// splitting a boolean out of a function creates two paths where there was one,
// and only the false side was reachable from the new code. A staged 1.1.0 must
// be superseded on disk by 1.2.0 here exactly as it is on demand.
func TestRunUpdateCheck_AutoStagesANewerRelease(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)
	writeStagedManifest(t, "1.1.0")
	stub := stubDownloadableRelease(t, "1.2.0")

	cfg := config.Default()
	cfg.Update.Check = true
	cfg.Update.Auto = true
	d := New(cfg)

	d.runUpdateCheck(true)

	if got := stub.archiveHits.Load(); got != 1 {
		t.Errorf("archive downloaded %d times, want 1 (the auto-stage)", got)
	}
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err != nil || man == nil {
		t.Fatalf("FindStaged = (%+v, %v), want the auto-staged release", man, err)
	}
	if man.Version != "1.2.0" {
		t.Errorf("staged version = %q, want 1.2.0", man.Version)
	}
	if vErr := update.VerifyStaged(dir, man, update.BinaryNames(runtime.GOOS)); vErr != nil {
		t.Errorf("VerifyStaged on the auto-staged release: %v", vErr)
	}
	if _, statErr := os.Stat(filepath.Join(config.UpdateDir(), "staged", "1.1.0")); !os.IsNotExist(statErr) {
		t.Errorf("staged/1.1.0 still present (stat err = %v), want pruned", statErr)
	}
	if st := update.LoadState(config.UpdateStatePath()); st.StagedVersion != "1.2.0" || st.LatestVersion != "1.2.0" {
		t.Errorf("state = %+v, want 1.2.0 as both latest and staged", st)
	}
	if info := d.currentUpdateInfo(); info == nil || info.StagedVersion != "1.2.0" {
		t.Errorf("currentUpdateInfo() = %+v, want 1.2.0 announced as staged", info)
	}
}

// TestHandleUpdateCheckReq_ConcurrentRequestsCheckOnce drives the single-flight
// CAS with real concurrency. The rate-limit test calls the handler
// sequentially, which proves the slot is RELEASED on both paths but never that
// it EXCLUDES — every caller there passes the CAS uncontended. A burst is the
// shape that matters: several attached clients open About at once, and all of
// them pass the rate check (nothing has been stamped yet) before contending.
func TestHandleUpdateCheckReq_ConcurrentRequestsCheckOnce(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)
	stub := stubDownloadableRelease(t, "1.1.0")

	cfg := config.Default()
	cfg.Update.Check = true
	d := New(cfg)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.handleUpdateCheckReq()
		}()
	}
	wg.Wait()
	waitForCheck(t, d)

	if got := stub.releaseHits.Load(); got != 1 {
		t.Errorf("release endpoint hit %d times from 8 concurrent requests, want 1", got)
	}
	// And the slot is free afterwards, or every later check is refused forever.
	if d.updateChecking.Load() {
		t.Error("updateChecking still held after the burst settled")
	}
}

// TestHandleUpdateCheckReq_RateLimited pins the bound on the check-only path.
// Opening the About dialog fires it, every attached client can do that as often
// as it likes, and unauthenticated GitHub allows 60 requests an hour — so a
// check that just ran must not be repeated on the next keypress.
func TestHandleUpdateCheckReq_RateLimited(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)
	stub := stubDownloadableRelease(t, "1.1.0")

	cfg := config.Default()
	cfg.Update.Check = true
	cfg.Update.Auto = true // the check-only path must not stage even so
	d := New(cfg)

	// First request goes through: nothing has been checked yet.
	d.handleUpdateCheckReq()
	waitForCheck(t, d)
	if got := stub.releaseHits.Load(); got != 1 {
		t.Fatalf("release endpoint hit %d times on the first request, want 1", got)
	}

	// Second request, immediately after: refused by the window.
	d.handleUpdateCheckReq()
	waitForCheck(t, d)
	if got := stub.releaseHits.Load(); got != 1 {
		t.Errorf("release endpoint hit %d times, want still 1 (inside the rate limit)", got)
	}

	// Check-only means check-only. The release IS downloadable and [update] auto
	// is on, so a path that staged would leave archiveHits at 1 and a staged dir
	// on disk — this assertion fails if allowStage is ever passed true here.
	if got := stub.archiveHits.Load(); got != 0 {
		t.Errorf("archive downloaded %d times, want 0 — a check must not stage", got)
	}
	if st := update.LoadState(config.UpdateStatePath()); st.StagedVersion != "" {
		t.Errorf("StagedVersion = %q, want empty (a check must not download)", st.StagedVersion)
	}
	if man, _, _ := update.FindStaged(config.UpdateDir()); man != nil {
		t.Errorf("FindStaged = %+v, want nothing staged by a check-only refresh", man)
	}
}

// TestHandleUpdateCheckReq_FailedCheckStillPaces is the reason the limiter
// keeps its own in-memory timestamp instead of reading LastCheckMs off disk.
//
// runUpdateCheck returns before it persists anything when the check errors —
// and the error that matters is GitHub answering 403 because the very quota
// this limiter protects is spent. Pacing on the persisted value therefore
// switched the limiter OFF exactly when it was needed: every About-dialog open
// issued another request, forever.
func TestHandleUpdateCheckReq_FailedCheckStillPaces(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	withVersionState(t, "1.0.0", true)

	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "rate limit exceeded", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	orig := newUpdateChecker
	newUpdateChecker = func() *update.Checker { return &update.Checker{BaseURL: srv.URL} }
	t.Cleanup(func() { newUpdateChecker = orig })

	cfg := config.Default()
	cfg.Update.Check = true
	d := New(cfg)

	for i := 0; i < 5; i++ {
		d.handleUpdateCheckReq()
		waitForCheck(t, d)
	}

	if got := hits.Load(); got != 1 {
		t.Errorf("throttled endpoint hit %d times from 5 About-opens, want 1 — a failing check must still pace", got)
	}
	if st := update.LoadState(config.UpdateStatePath()); st.LastCheckMs != 0 {
		t.Errorf("LastCheckMs = %d, want 0 — a failed check persists nothing, which is why pacing cannot rely on it", st.LastCheckMs)
	}
}

// waitForCheck blocks until the check-only worker has released its
// single-flight slot, so assertions run against a settled daemon.
func waitForCheck(t *testing.T, d *Daemon) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !d.updateChecking.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("update check did not finish within 5s")
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
