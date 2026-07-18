package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

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
	if !versionpkg.IsRelease() {
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
	// Corruption/tamper gate: re-hash staged files against the manifest.
	if err := update.VerifyStaged(dir, man); err != nil {
		log.Printf("staged update v%s failed verification: %v — discarding", man.Version, err)
		os.RemoveAll(dir)
		return false
	}
	if !preConfirmed && !promptApplyUpdate(man.Version) {
		return false
	}
	if err := swapBinaries(dir); err != nil {
		fmt.Fprintf(os.Stderr, "update to v%s failed: %v — continuing on v%s\n",
			man.Version, err, versionpkg.Current())
		return false
	}
	log.Printf("update: swapped binaries to v%s, respawning", man.Version)
	return respawnSelf()
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
		return false
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "" || a == "y" || a == "yes"
}

// swapBinaries installs the staged quil and quild over the live install.
// If the second swap fails, the first is rolled back so the pair never
// splits versions.
func swapBinaries(stagedDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	names := update.BinaryNames(runtime.GOOS)
	quilName, quildName := names[0], names[1]

	quilTarget := exe
	quildTarget := findDaemonBinaryForUpgrade()
	if !filepath.IsAbs(quildTarget) {
		return fmt.Errorf("cannot locate installed quild (got %q)", quildTarget)
	}

	if err := swapOne(quilTarget, filepath.Join(stagedDir, quilName)); err != nil {
		return err
	}
	if err := swapOne(quildTarget, filepath.Join(stagedDir, quildName)); err != nil {
		// Roll the first swap back so quil/quild stay version-matched.
		os.Remove(quilTarget)
		if rbErr := os.Rename(quilTarget+".old", quilTarget); rbErr != nil {
			return fmt.Errorf("%w (AND quil rollback failed: %v — restore %s.old manually)", err, rbErr, quilTarget)
		}
		return err
	}
	return nil
}

// swapOne backs the target up as <target>.old (renaming a running
// executable is legal on Windows — NT locks the image by open handle, not
// path) and copies the staged binary into place. On failure the backup is
// renamed back.
func swapOne(target, staged string) error {
	backup := target + ".old"
	os.Remove(backup) // stale backup from a previous update (best-effort)
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("back up %s: %w", target, err)
	}
	if err := copyFile(staged, target); err != nil {
		if rbErr := os.Rename(backup, target); rbErr != nil {
			return fmt.Errorf("install %s: %w (AND rollback failed: %v — restore %s manually)", target, err, rbErr, backup)
		}
		return fmt.Errorf("install %s: %w", target, err)
	}
	return nil
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

// respawnSelf runs the freshly-installed quil with the original args and
// QUIL_UPDATE_RESTART=1 (the version gate reads it to skip the second
// restart prompt). This process stays as a thin wrapper waiting for the
// child — exiting immediately would hand the terminal back to the shell
// while the child TUI still owns it. Always returns true: the swap is
// already done, so even on spawn failure the caller must not fall through
// to running the (renamed-away) old binary's launch path — the user just
// relaunches manually.
func respawnSelf() bool {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "update applied — relaunch quil manually (%v)\n", err)
		return true
	}
	cmd := exec.Command(exe, os.Args[1:]...)
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

// cleanupAppliedUpdate removes .old backups and the staged dir once the
// running version has caught up with the staged one. Best-effort: on
// Windows the wrapper parent from the apply respawn may still hold
// quil.exe.old open as its process image, so deletion can fail — the next
// launch (no wrapper) retries.
func cleanupAppliedUpdate() {
	man, dir, err := update.FindStaged(config.UpdateDir())
	if err == nil && man != nil {
		if cmp, cErr := versionpkg.Compare(man.Version, versionpkg.Current()); cErr == nil && cmp <= 0 {
			os.RemoveAll(dir)
		}
	}
	if exe, exeErr := os.Executable(); exeErr == nil {
		os.Remove(exe + ".old")
	}
	if quild := findDaemonBinaryForUpgrade(); filepath.IsAbs(quild) {
		os.Remove(quild + ".old")
	}
}

// updateRestartPreapproved reports whether this process was respawned by
// the apply path — the user already confirmed the daemon restart there, so
// the version gate must not prompt a second time.
func updateRestartPreapproved() bool {
	return os.Getenv("QUIL_UPDATE_RESTART") == "1"
}
