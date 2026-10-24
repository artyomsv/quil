package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/update"
	"github.com/artyomsv/quil/internal/version"
)

// The Linux quild a container's hooks invoke.
//
// The hook binary itself needs no change to run in a container — it reads JSON
// on stdin and writes files under $QUIL_HOOK_HOME — but a Windows or macOS
// daemon has no Linux build of itself to mount. One is fetched from the same
// GitHub release this daemon was built from, so the two are the same version:
// the hook writes a session record the daemon reads back, and a version skew
// there is a format skew.

// sandboxQuildEnv overrides the fetch with a locally built binary.
//
// Required for development, not a convenience: the fetch resolves a PUBLISHED
// release, and a dev build's version is not one (internal/daemon/update.go
// gates every release path on version.IsRelease). Without this a developer
// could never exercise sandbox hooks at all.
const sandboxQuildEnv = "QUIL_SANDBOX_QUILD"

// sandboxBinDir is where fetched binaries live, keyed by version.
//
// NEVER config.UpdateDir(). The auto-update apply path scans that tree and
// installs whatever it finds, and on a Linux host a linux archive's binary
// names are identical to a real update's — so staging here would let a sandbox
// fetch be applied as an upgrade, and would corrupt FindStaged's
// highest-version pick either way.
func sandboxBinDir(quilDir, ver string) string {
	return filepath.Join(sandboxRoot(quilDir), "bin", ver)
}

// quildFetchFn is the seam the tests drive. Production stages a release; a
// test hands back a path without touching the network.
var quildFetchFn = stageLinuxQuild

// quildFetchMu serialises fetches so N panes starting at once produce one
// download rather than N writing over each other.
var quildFetchMu sync.Mutex

// ensureLinuxQuild returns the host path of a Linux quild matching this
// daemon's version, fetching it once per version if needed.
func ensureLinuxQuild(ctx context.Context, arch string) (string, error) {
	if p := os.Getenv(sandboxQuildEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%s: %w", sandboxQuildEnv, p, err)
		}
		return p, nil
	}
	if arch == "" {
		arch = "amd64"
	}
	ver := version.Current()
	if !version.IsRelease() {
		return "", fmt.Errorf("this is a development build (%s); set %s to a linux quild to use sandbox hooks",
			ver, sandboxQuildEnv)
	}

	quildFetchMu.Lock()
	defer quildFetchMu.Unlock()

	dst := filepath.Join(sandboxBinDir(config.QuilDir(), ver), "quild-linux-"+arch)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := quildFetchFn(ctx, ver, arch, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// quildFetchTimeout bounds the whole download-and-extract.
//
// spawnPane can run on the requesting client's IPC dispatch goroutine, which
// processes that connection's messages SEQUENTIALLY — so an unbounded fetch
// there stops that client's keystrokes to every pane for as long as it takes.
// update.Stager's own per-request timeout is generous by design (an update is
// a background task nobody is waiting on); a pane create is not.
const quildFetchTimeout = 90 * time.Second

// stageLinuxQuild downloads and verifies the release archive for linux/<arch>
// and extracts quild out of it.
//
// It goes through update.Stager rather than the package's lower-level helpers:
// downloadHashed, fetchChecksums and extractBinaries are all unexported, and
// Stage is already parameterised on GOOS/GOARCH, so a linux stage needs no new
// download machinery and inherits the checksum verification unchanged.
func stageLinuxQuild(ctx context.Context, ver, arch, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, quildFetchTimeout)
	defer cancel()

	checker := &update.Checker{}
	rel, err := checker.Release(ctx, "v"+ver)
	if err != nil {
		if errors.Is(err, update.ErrReleaseNotFound) {
			// The typed 404 names the tag alone; a dev build's version, or
			// a release pulled after the fact, is better reported with the
			// repository it was looked for in.
			return fmt.Errorf("release v%s not found in %s: %w", ver, update.DefaultRepo, err)
		}
		return fmt.Errorf("locate release v%s: %w", ver, err)
	}
	root := filepath.Dir(dst)
	stager := &update.Stager{Root: root, GOOS: "linux", GOARCH: arch}
	if err := stager.Stage(ctx, rel); err != nil {
		return fmt.Errorf("stage linux quild: %w", err)
	}
	// Stage writes <root>/staged/<version>/; lift the one binary we want to
	// the stable path and drop the rest. The archive also carries `quil`,
	// which is harmless here and simply unwanted.
	staged := filepath.Join(root, "staged", ver, "quild")
	body, err := os.ReadFile(staged)
	if err != nil {
		return fmt.Errorf("read staged quild: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(dst, body, 0o700); err != nil {
		return fmt.Errorf("install linux quild: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "staged")); err != nil {
		log.Printf("sandbox: clean staged dir: %v", err)
	}
	return nil
}

// linuxQuildForPane resolves the hook binary for a sandbox pane, and reports
// whether the pane may spawn without one.
//
// A claude pane may NOT. Running hookless is not a degraded mode there, it is
// a delayed crash: with no .id record a later restart takes the hadSession
// branch, locates nothing, and passes --session-id for an id whose transcript
// exists — exit 129. Better to refuse now, with Alt+R as the retry, than to
// work for one session and die on the next.
func (d *Daemon) linuxQuildForPane(ctx context.Context, pluginName, arch string) (string, error) {
	path, err := ensureLinuxQuild(ctx, arch)
	if err == nil {
		return path, nil
	}
	if plugin.UsesClaudeAuthName(pluginName) {
		return "", fmt.Errorf("hook binary unavailable: %w", err)
	}
	// Other plugins lose session tracking and notifications, which is
	// visible and recoverable. Say so once rather than failing the spawn.
	log.Printf("sandbox: %s pane will run without hooks: %v", pluginName, err)
	return "", nil
}
