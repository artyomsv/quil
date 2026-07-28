package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/update"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// maybeApplyStagedUpdate applies a fully-staged newer release: verify →
// prompt (skipped when preConfirmed — the TUI confirm already asked) →
// swap binaries → respawn quil. Returns true when the caller must return
// immediately because the new binary ran (this process acted as a thin
// wrapper and the session is over). Every failure path rolls back and
// returns false so the old version launches normally.
func maybeApplyStagedUpdate(preConfirmed bool) bool {
	if !versionpkg.IsRelease() || !versionpkg.UpdatesEnabled() {
		return false
	}
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err != nil || man == nil {
		return false
	}
	cmp, err := versionpkg.Compare(man.Version, versionpkg.Current())
	if err != nil || cmp <= 0 {
		return false
	}
	// Corruption/tamper gate: re-hash staged files against the manifest, and
	// require it to cover BOTH binaries swapPair is about to install — a
	// manifest that simply omits one would otherwise let it through unhashed.
	if err := update.VerifyStaged(dir, man, update.BinaryNames(runtime.GOOS)); err != nil {
		log.Printf("staged update v%s failed verification: %v — discarding", man.Version, err)
		os.RemoveAll(dir)
		return false
	}
	if !preConfirmed && !promptApplyUpdate(man.Version) {
		return false
	}

	// Resolve the running executable's path exactly ONCE, before any swap,
	// and thread it through to both swapBinaries and respawnSelf. On Linux
	// os.Executable() is a live `readlink /proc/self/exe`; once swapOne
	// below renames this binary aside to "<exe>.old", a SECOND call to
	// os.Executable() re-resolves to that renamed path — respawnSelf would
	// then relaunch the OLD binary, which re-detects the still-staged
	// update and prompts again (and can corrupt the install layout on the
	// resulting double-apply). Do not "simplify" this back to calling
	// os.Executable() inside swapBinaries/respawnSelf.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "update to v%s failed: locate own executable: %v — continuing on v%s\n",
			man.Version, err, versionpkg.Current())
		return false
	}
	// Hold the lock across the swap only. respawnSelf below blocks for the
	// whole child session, and the swap is finished by then.
	release, locked := acquireApplyLock(config.UpdateDir())
	if !locked {
		fmt.Fprintf(os.Stderr, "another quil is applying an update right now — continuing on v%s\n",
			versionpkg.Current())
		return false
	}
	swapErr := swapBinaries(exe, dir)
	release()
	if swapErr != nil {
		fmt.Fprintf(os.Stderr, "update to v%s failed: %v — continuing on v%s\n",
			man.Version, swapErr, versionpkg.Current())
		return false
	}
	log.Printf("update: swapped binaries to v%s, respawning", man.Version)
	return respawnSelf(exe)
}

// promptApplyUpdate asks on the terminal, version-gate style. Default is
// YES (plain Enter applies): consent to auto-update was given via
// [update] auto = true, and this prompt fires at a natural restart moment.
func promptApplyUpdate(ver string) bool {
	fmt.Fprintf(os.Stderr,
		"\n"+
			"  Quil update ready.\n"+
			"\n"+
			"    Installed: %s\n"+
			"    Staged:    %s\n"+
			"\n"+
			"  Applying restarts the daemon; panes respawn (claude sessions\n"+
			"  resume, in-flight shell commands are killed).\n"+
			"\n"+
			"  Apply now? [Y/n] ",
		versionpkg.Current(), ver,
	)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("prompt read: %v", err)
		return false
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "" || a == "y" || a == "yes"
}

// swapBinaries installs the staged quil and quild over the live install.
// exe is the caller-resolved path to the running quil executable (resolved
// exactly once in maybeApplyStagedUpdate — see the comment there). If the
// second swap fails, the first is rolled back so the pair never splits
// versions.
func swapBinaries(exe, stagedDir string) error {
	quildTarget := findDaemonBinaryForUpgrade()
	if !filepath.IsAbs(quildTarget) {
		return fmt.Errorf("cannot locate installed quild (got %q)", quildTarget)
	}
	return swapPair(exe, quildTarget, stagedDir, runtime.GOOS)
}

// swapPair swaps both binaries as a unit: if the second swap fails, the
// first is rolled back so quil/quild never split versions. Pure function
// of its arguments (target resolution via os.Executable /
// findDaemonBinaryForUpgrade lives in swapBinaries) so the rollback path
// is directly testable against temp-dir targets.
func swapPair(quilTarget, quildTarget, stagedDir, goos string) error {
	names := update.BinaryNames(goos)
	quilName, quildName := names[0], names[1]

	quilBackup, err := swapOne(quilTarget, filepath.Join(stagedDir, quilName))
	if err != nil {
		return err
	}
	if _, err := swapOne(quildTarget, filepath.Join(stagedDir, quildName)); err != nil {
		// Roll the first swap back so quil/quild stay version-matched, from
		// the path swapOne actually used — it is not always "<target>.old"
		// (see freeBackupPath). os.Rename replaces an existing destination
		// on both Windows and POSIX, so quilTarget (holding the
		// just-installed, non-running new binary) doesn't need removing
		// first — an unchecked Remove here would leave NO quil binary on
		// disk if this rename failed.
		if rbErr := os.Rename(quilBackup, quilTarget); rbErr != nil {
			return fmt.Errorf("%w (AND quil rollback failed: %v — restore %s manually)", err, rbErr, quilBackup)
		}
		return err
	}
	return nil
}

const (
	// applyLockName marks a swap in progress, inside config.UpdateDir().
	applyLockName = "apply.lock"
	// applyLockStale is when a lock counts as abandoned. The swap it guards is
	// one rename plus one file copy per binary — orders of magnitude below
	// this — so a lock older than that belongs to a process that died mid-swap
	// and must not wedge updates forever.
	applyLockStale = 2 * time.Minute
)

// acquireApplyLock takes an exclusive marker for the duration of the binary
// swap, returning a release func and whether it was taken.
//
// Two `quil` processes starting together both pass verification and both swap.
// That alone is survivable, but each one's cleanup sweep deletes backups by
// name — so P2 can unlink the backup P1 is mid-swap on, and if P1's copy then
// fails, its rollback finds nothing and NO binary is left at the target path.
// The racing parties are separate processes, so the exclusion has to live on
// disk rather than in a mutex.
func acquireApplyLock(dir string) (release func(), ok bool) {
	noop := func() {}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return noop, false
	}
	path := filepath.Join(dir, applyLockName)
	self := strconv.Itoa(os.Getpid())
	// Two attempts: the second exists only to claim a lock found abandoned on
	// the first. If another process wins that race, O_EXCL fails again and we
	// back off rather than looping.
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintln(f, self)
			f.Close()
			// Release only OUR lock. If this swap somehow outran the staleness
			// window, another process may already hold the path, and removing
			// it by name would hand a third process a lock while the second is
			// mid-swap — precisely the interleaving this exists to prevent.
			return func() {
				if data, rErr := os.ReadFile(path); rErr == nil &&
					strings.TrimSpace(string(data)) == self {
					os.Remove(path)
				}
			}, true
		}
		if !applyLockAbandoned(path) {
			return noop, false
		}
		os.Remove(path)
	}
	return noop, false
}

// applyLockAbandoned reports whether the lock at path can be taken over.
//
// The recorded pid makes this mostly a fact rather than a guess: a lock whose
// holder is gone is reclaimable immediately, without waiting out the age
// window. The age cap stays as the backstop for the two cases liveness cannot
// answer — an unparseable/legacy lock file, and a pid since recycled by an
// unrelated long-running process, which would otherwise read as "held"
// forever. A live holder older than the cap is therefore still treated as
// abandoned; the swap it guards is two file copies, so it cannot legitimately
// run that long, and the ownership check in release() keeps the takeover safe
// if it somehow does.
func applyLockAbandoned(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false // cannot judge it — leave it alone
	}
	if data, rErr := os.ReadFile(path); rErr == nil {
		if pid, pErr := strconv.Atoi(strings.TrimSpace(string(data))); pErr == nil && pid > 0 {
			if alive, _ := processProbe(pid); !alive {
				return true
			}
		}
	}
	return time.Since(info.ModTime()) > applyLockStale
}

// applyInProgress reports whether another process holds a live apply lock.
func applyInProgress(dir string) bool {
	path := filepath.Join(dir, applyLockName)
	if _, err := os.Stat(path); err != nil {
		// Fail CLOSED on anything but "no lock": if the lock cannot even be
		// stat'd, we cannot prove no swap is running, and the cost of
		// skipping a cleanup sweep is a few leftover files.
		return !errors.Is(err, fs.ErrNotExist)
	}
	return !applyLockAbandoned(path)
}

// maxBackupSlots bounds the ".old.N" search in freeBackupPath. Reaching it
// means N surviving processes each pin a distinct backup — a real problem
// worth reporting rather than probing forever.
const maxBackupSlots = 20

// freeBackupPath returns a path that can receive target's backup, clearing a
// stale backup left by an earlier update when it can. The canonical
// "<target>.old" is preferred and recycled whenever it is deletable.
//
// When it is NOT deletable, fall back to "<target>.old.1", ".2", … On Windows
// a backup is unremovable whenever some process still runs it as its image:
// an earlier update renamed a live quild aside, and that daemon kept running
// from the renamed file. NT refuses DELETE on a mapped image, which fails
// os.Remove AND the rename-replace that follows (MOVEFILE_REPLACE_EXISTING
// must delete the destination first) — both with "Access is denied". Without
// a fallback, one such leftover wedges EVERY future update permanently, since
// nothing ever clears it. Third-party handles (antivirus, indexers) pin files
// the same way, so this is not specific to orphaned daemons.
func freeBackupPath(target string) (string, error) {
	for i := 0; i <= maxBackupSlots; i++ {
		path := target + ".old"
		if i > 0 {
			path = fmt.Sprintf("%s.%d", path, i)
		}
		os.Remove(path) // best-effort: a pinned backup survives this
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no free backup slot for %s (%s.old and .1-.%d are all occupied and undeletable)",
		target, target, maxBackupSlots)
}

// swapOne backs the target up (renaming a running executable is legal on
// Windows — NT locks the image by open handle, not path) and copies the
// staged binary into place. Returns the backup path actually used so the
// caller can roll back; it is usually "<target>.old" but see freeBackupPath.
// On failure the backup is renamed back.
func swapOne(target, staged string) (string, error) {
	backup, err := freeBackupPath(target)
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", target, err)
	}
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("back up %s: %w", target, err)
	}
	if err := copyFile(staged, target); err != nil {
		if rbErr := os.Rename(backup, target); rbErr != nil {
			return "", fmt.Errorf("install %s: %w (AND rollback failed: %v — restore %s manually)", target, err, rbErr, backup)
		}
		return "", fmt.Errorf("install %s: %w", target, err)
	}
	return backup, nil
}

// sameDir reports whether two files live in the same directory, comparing by
// identity rather than by string. exec.LookPath can hand back a different
// casing — or an 8.3 short name on Windows — for the very directory
// os.Executable() resolved to, and a byte compare would then silently skip the
// sweep in the one branch the guard exists for.
func sameDir(a, b string) bool {
	fa, err := os.Stat(filepath.Dir(a))
	if err != nil {
		return false
	}
	fb, err := os.Stat(filepath.Dir(b))
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// removeBackups deletes every backup swapOne may have left for target — the
// canonical "<target>.old" plus any ".old.N" fallbacks. Best-effort and
// gap-tolerant (slot 1 can outlive slot 2): a backup still pinned by a
// surviving process stays, and a later launch retries. Built by construction
// rather than filepath.Glob so an install path containing glob metacharacters
// ("[", "*") still matches its own backups.
func removeBackups(target string) {
	os.Remove(target + ".old")
	for i := 1; i <= maxBackupSlots; i++ {
		os.Remove(fmt.Sprintf("%s.old.%d", target, i))
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	if closeErr := out.Close(); cpErr == nil {
		cpErr = closeErr
	}
	return cpErr
}

// respawnArgs rebuilds the child's argv for an update respawn.
//
// os.Args cannot be used directly: main() STRIPS --remote from it (remote.go's
// parseRemoteFlag, applied at main.go) and records the destination only in the
// process-local remoteDest. --dev survives the same rewrite because it is
// re-exported as QUIL_HOME and respawnSelf passes os.Environ(); --remote has no
// such carrier, so respawning the rewritten argv drops it silently and the
// replacement process attaches to — or starts — the LOCAL daemon while the
// operator believes they are still on the remote host. In a project whose panes
// routinely run `claude --dangerously-skip-permissions`, keystrokes then land
// on the wrong machine with nothing on screen to contradict it.
func respawnArgs() []string {
	args := os.Args[1:]
	if !remoteMode() {
		return args
	}
	// Rebuilt rather than restored from a saved copy so the flag is re-emitted
	// in the canonical two-token form regardless of which spelling the user
	// typed (--remote X or --remote=X).
	return append([]string{"--remote", remoteDest}, args...)
}

// respawnSelf runs the freshly-installed quil at exe (the same path
// maybeApplyStagedUpdate resolved BEFORE the swap — see the comment there;
// re-resolving via os.Executable() here would return the just-renamed-aside
// ".old" binary on Linux) with the original args and QUIL_UPDATE_RESTART=1
// (the version gate reads it to skip the second restart prompt). This
// process stays as a thin wrapper waiting for the child — exiting
// immediately would hand the terminal back to the shell while the child TUI
// still owns it. Always returns true: the swap is already done, so even on
// spawn failure the caller must not fall through to running the
// (renamed-away) old binary's launch path — the user just relaunches
// manually.
func respawnSelf(exe string) bool {
	cmd := exec.Command(exe, respawnArgs()...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "QUIL_UPDATE_RESTART=1")
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "update applied but relaunch failed: %v — run quil again\n", err)
	}
	return true
}

// cleanupAppliedUpdate removes backups and the staged dir once the running
// version has caught up with the staged one. Best-effort: on Windows the
// wrapper parent from the apply respawn may still hold quil.exe.old open as
// its process image, so deletion can fail — the next launch (no wrapper)
// retries, and a backup pinned for good no longer blocks anything because
// freeBackupPath routes around it.
func cleanupAppliedUpdate() {
	// Never sweep while another process is mid-swap: its in-flight backup is
	// named exactly like a leftover, and deleting it strands that process with
	// no way to roll back.
	if applyInProgress(config.UpdateDir()) {
		return
	}
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err == nil && man != nil {
		if cmp, cErr := versionpkg.Compare(man.Version, versionpkg.Current()); cErr == nil && cmp <= 0 {
			os.RemoveAll(dir)
		}
	}
	exe, exeErr := os.Executable()
	if exeErr != nil {
		return
	}
	removeBackups(exe)
	// Only sweep the daemon's backups when it sits next to us.
	// findDaemonBinaryForUpgrade falls through to a PATH lookup, and deleting
	// "<path>.old[.N]" in a directory this install does not own is not ours to
	// do — the more so now that the sweep covers the numbered slots too.
	if quild := findDaemonBinaryForUpgrade(); filepath.IsAbs(quild) && sameDir(quild, exe) {
		removeBackups(quild)
	}
}

// updateRestartPreapproved reports whether this process was respawned by
// the apply path — the user already confirmed the daemon restart there, so
// the version gate must not prompt a second time.
func updateRestartPreapproved() bool {
	return os.Getenv("QUIL_UPDATE_RESTART") == "1"
}
