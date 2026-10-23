// Package toolupdate replaces a third-party tool's binary (the Stripe CLI is
// the first user) with a release asset fetched by tag, verified against the
// upstream's published checksum and swapped in with the rename-aside swap
// Quil's own update uses. It is the logic behind `quild plugin-update`, which
// the daemon runs as a pane's child process so the user watches its output.
//
// Platform-neutral on purpose: the OS and the environment are parameters, so
// the Linux CI exercises the windows decisions too. Nothing here reads
// QUIL_HOME, config or the plugin registry — the daemon resolved all of that
// and passed the result as flags.
package toolupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/binswap"
	"github.com/artyomsv/quil/internal/update"
)

// detectTimeout bounds each `[command] detect` run. A binary that hangs on
// --version must not hang the update — detection is informational.
const detectTimeout = 10 * time.Second

// downloadTimeout bounds the whole fetch (release JSON, archive, checksums).
const downloadTimeout = 5 * time.Minute

// unknownVersion stands in for a version detect could not produce.
const unknownVersion = "unknown"

// Options is everything Run needs, all of it decided by the daemon.
type Options struct {
	Target    string // path of the binary to replace (symlinks already resolved)
	Repo      string // GitHub "owner/name"
	Tag       string // release tag, "v1.55.1"; never empty — there is no latest fallback
	Asset     string // archive asset name in the release
	Checksums string // checksums file asset name in the release
	Binary    string // bare file name inside the archive (".exe" appended on windows)
	DetectCmd string // optional "stripe --version"; the first word is replaced by Target
	GOOS      string // runtime.GOOS at the call site

	BaseURL string              // GitHub API base; tests point it at httptest
	Env     func(string) string // environment for the system-location guard; nil → os.Getenv
}

// Run performs one update, printing each step to out as one line. The
// order is the spec's: sweep earlier backups, refuse a system location,
// check the target directory is writable, detect the old version, fetch
// the release by tag, download and verify, extract, swap, detect the new
// version. Any refusal leaves the target untouched; a failed swap is rolled
// back by binswap.SwapOne. The error returned is the one line the caller
// prints.
func Run(ctx context.Context, o Options, out io.Writer) error {
	if err := o.validate(); err != nil {
		return err
	}
	step := func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }

	target := o.Target
	// The daemon resolves symlinks before deciding on the manual path, but
	// resolve again here so the guard and the swap see the real file when
	// the subcommand is run by hand; a path that does not resolve is
	// examined as given (the guard may still refuse it).
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}

	// Every refusal comes BEFORE anything is deleted: a target this run
	// will not touch must not lose its backups or its neighbours either.
	env := o.Env
	if env == nil {
		env = os.Getenv
	}
	if SystemLocation(target, o.GOOS, env) {
		return fmt.Errorf("installed by a package manager Quil does not know (%s); update it with that manager", target)
	}

	// The writability refusal comes BEFORE the lock: the lock is a file in
	// this directory, and asking for it first would turn "not writable" into
	// a create error (or, worse, a wait for a holder that is not there).
	dir := filepath.Dir(target)
	if !dirWritable(dir) {
		return fmt.Errorf("target directory not writable: %s", dir)
	}

	// One updater per target at a time, across processes: two panes on one
	// tool can each start this subcommand, and RestoreMissing, RemoveBackups
	// and SwapOne share "<target>.old" and "<target>.new" — the second would
	// restore the "missing" target from the first's backup mid-swap and the
	// loser would roll its backup over the winner's install. Taken before
	// anything beside the target is read or touched, held through the swap,
	// released on every path. A second updater waits for the first, bounded;
	// any failure other than a live holder is reported at once.
	release, err := binswap.WaitLock(ctx, target+updateLockSuffix, updateLockStale, updateLockWait, func() {
		step("→ waiting for another update of %s…", filepath.Base(target))
	})
	if err != nil {
		return fmt.Errorf("lock %s: %w", target, err)
	}
	defer release()

	// A run killed between its rename-aside and its copy leaves the target
	// path empty and the binary in the backup slot; the next run puts it
	// back before anything else looks for it, or the user is told "no such
	// file" about a tool whose only copy sits beside the path.
	if restored, err := binswap.RestoreMissing(target); err != nil {
		return fmt.Errorf("target: %w", err)
	} else if restored {
		step("→ restored %s from the backup an interrupted update left", filepath.Base(target))
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("target is not a regular file: %s", target)
	}

	step("→ clearing earlier backups and staging leftovers of %s", filepath.Base(target))
	binswap.RemoveBackups(target)
	sweepStaging(dir, time.Now())

	oldVer := detectVersion(ctx, o.DetectCmd, target, step)
	step("→ current version: %s", oldVer)

	// Stage beside the target when possible so the final copy stays on one
	// volume; a directory that takes files but refuses a subdirectory (an
	// odd ACL) falls back to the system temp dir. Always removed.
	staging, err := os.MkdirTemp(dir, stagingPrefix)
	if err != nil {
		if staging, err = os.MkdirTemp("", "quil-update-"); err != nil {
			return fmt.Errorf("create staging dir: %w", err)
		}
	}
	defer os.RemoveAll(staging)

	fetchCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	client := &http.Client{Timeout: downloadTimeout}

	step("→ fetching release %s from %s", o.Tag, o.Repo)
	checker := &update.Checker{BaseURL: o.BaseURL, Repo: o.Repo, Client: client}
	rel, err := checker.Release(fetchCtx, o.Tag)
	if err != nil {
		if errors.Is(err, update.ErrReleaseNotFound) {
			return fmt.Errorf("release tag %s not found in %s: %w", o.Tag, o.Repo, update.ErrReleaseNotFound)
		}
		return fmt.Errorf("release %s of %s: %w", o.Tag, o.Repo, err)
	}

	step("→ downloading %s", o.Asset)
	archive, err := update.FetchVerified(fetchCtx, client, rel, o.Asset, o.Checksums, staging)
	if err != nil {
		return describeFetchError(err, o)
	}
	step("✓ checksum ok")

	step("→ extracting %s", o.Binary)
	staged, err := update.ExtractBinary(archive, o.Binary, o.GOOS, staging)
	if err != nil {
		return fmt.Errorf("extract %s from %s: %w", o.Binary, o.Asset, err)
	}

	step("→ replacing %s", target)
	if _, err := binswap.SwapOne(target, staged); err != nil {
		return err
	}

	newVer := detectVersion(ctx, o.DetectCmd, target, step)
	step("✓ updated %s: %s → %s", target, oldVer, newVer)
	return nil
}

// validate refuses an incomplete Options before anything is touched. The
// empty-Tag refusal is the "no latest fallback" guarantee: Checker.Release
// with an empty tag fetches /releases/latest, and the dialog that offered
// a version must install that version or nothing.
func (o Options) validate() error {
	required := []struct{ name, val string }{
		{"target", o.Target},
		{"repo", o.Repo},
		{"tag", o.Tag},
		{"asset", o.Asset},
		{"checksums", o.Checksums},
		{"binary", o.Binary},
	}
	for _, r := range required {
		if strings.TrimSpace(r.val) == "" {
			return fmt.Errorf("plugin-update: --%s is required", r.name)
		}
	}
	if o.GOOS == "" {
		return errors.New("plugin-update: GOOS is required")
	}
	return nil
}

// stagingPrefix names everything this package creates beside the target —
// the staging dir and the writability probe — so sweepStaging can tell its
// own leftovers from the user's files by name alone.
const stagingPrefix = ".quil-update-"

const (
	// updateLockSuffix names the per-target lock beside it ("stripe.lock"):
	// keyed by the target's own name, and outside stagingPrefix so the age
	// sweep never takes a live lock.
	updateLockSuffix = ".lock"
	// updateLockWait bounds how long a second updater waits for the first.
	updateLockWait = 5 * time.Minute
	// updateLockStale is when a lock with a live-looking pid counts as
	// abandoned anyway (the backstop for a recycled pid): a run is bounded
	// by downloadTimeout plus two detects and a swap, so twice the download
	// is a run that is not there.
	updateLockStale = 2 * downloadTimeout
)

// dirWritable asks the filesystem the question the swap is about to ask —
// create a file in dir — rather than reading permission bits, which say
// nothing on Windows and nothing about a read-only mount.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, stagingPrefix+"probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// staleStagingAge is how old a staging entry must be before sweepStaging
// takes it. Two panes on one binary can press Update at once, and the
// second run's sweep must not take the first run's LIVE staging dir out
// from under its download. A live run is bounded by downloadTimeout (5
// min) plus the detect and swap, so an entry an hour old belongs to a run
// that is gone.
const staleStagingAge = time.Hour

// sweepStaging removes staging dirs and probe files an earlier run left
// beside the target — a run killed mid-download otherwise litters the
// install dir forever, since nothing else knows the name. Only entries
// older than staleStagingAge go (see there); the probe file is covered by
// the same rule, and dirWritable removes its own anyway. Best-effort;
// os.ReadDir + a prefix match rather than filepath.Glob, so an install path
// holding glob metacharacters still sweeps itself (binswap's reasoning).
func sweepStaging(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), stagingPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleStagingAge {
			continue
		}
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// describeFetchError turns FetchVerified's typed refusals into the one line
// a user reads in the pane, naming what was missing or wrong.
func describeFetchError(err error, o Options) error {
	switch {
	case errors.Is(err, update.ErrNoAsset):
		return fmt.Errorf("release %s has no asset for this platform: %w", o.Tag, err)
	case errors.Is(err, update.ErrNoChecksum):
		return fmt.Errorf("%s has no checksum for %s — refusing an unverified download: %w", o.Checksums, o.Asset, err)
	case errors.Is(err, update.ErrChecksumMismatch):
		return fmt.Errorf("%s does not match its published checksum — the target was left untouched: %w", o.Asset, err)
	}
	return fmt.Errorf("download %s: %w", o.Asset, err)
}

// detectVersion runs the plugin's detect command with target in place of
// its first word and returns the first line it prints, or unknownVersion.
// A failure is reported through step and never fails the update: the
// version is what the user is shown, not what the swap depends on.
func detectVersion(ctx context.Context, detectCmd, target string, step func(string, ...any)) string {
	words := strings.Fields(detectCmd)
	if len(words) == 0 {
		return unknownVersion
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, words[1:]...)
	cmd.Dir = filepath.Dir(target)
	outBytes, err := cmd.CombinedOutput()
	line := firstLine(outBytes)
	if err != nil {
		step("! detect (%s): %v", strings.Join(append([]string{filepath.Base(target)}, words[1:]...), " "), err)
	}
	if line == "" {
		return unknownVersion
	}
	return line
}

// firstLine returns the first non-blank line of b, trimmed, with control
// and direction-override characters dropped: the line is printed into the
// pane and later shown by the daemon, and a tool's banner must not redraw
// the screen (C0/C1, DEL) or reverse the text around it (bidi overrides and
// isolates, which are PRINTABLE and pass any control-character filter —
// internal/tui/remotetext.go's reasoning).
func firstLine(b []byte) string {
	for _, raw := range bytes.Split(b, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		return strings.Map(func(r rune) rune {
			if dropRune(r) {
				return -1
			}
			return r
		}, line)
	}
	return ""
}

// dropRune reports whether r is a C0/C1 control, DEL, or a bidi control
// (LRM/RLM, the LRE…RLO overrides and the LRI…PDI isolates).
func dropRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x200e, r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
