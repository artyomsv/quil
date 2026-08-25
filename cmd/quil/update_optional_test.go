package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/update"
)

// The activation helper installs BESIDE quil.exe, because that is the only
// place `quil notify setup` looks for it (notify.ActivateHelperName, resolved
// against filepath.Dir of the binary being registered — the same rule
// findDaemonBinary follows). Landing it anywhere else is landing it nowhere.
func TestSwapPair_InstallsActivateHelper(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quild.exe"), []byte("new-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("new-activate"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, manifestDeclaring("quil.exe", "quild.exe", "quil-activate.exe"), "windows"); err != nil {
		t.Fatalf("swapPair() = %v, want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(installDir, "quil-activate.exe"))
	if err != nil || string(got) != "new-activate" {
		t.Fatalf("installed quil-activate.exe = %q (err %v), want new-activate — "+
			"a toast click falls back to the console binary without it", got, err)
	}
}

// An existing helper is REPLACED, and through swapOne rather than a bare copy:
// Windows refuses to overwrite an executable while some process still runs it
// as its image, and a click that fired a second ago is exactly that.
func TestSwapPair_ReplacesExistingActivateHelper(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	helperTarget := filepath.Join(installDir, "quil-activate.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)
	os.WriteFile(helperTarget, []byte("old-activate"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quild.exe"), []byte("new-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("new-activate"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, manifestDeclaring("quil.exe", "quild.exe", "quil-activate.exe"), "windows"); err != nil {
		t.Fatalf("swapPair() = %v, want nil", err)
	}
	got, err := os.ReadFile(helperTarget)
	if err != nil || string(got) != "new-activate" {
		t.Errorf("helper after swap = %q (err %v), want new-activate", got, err)
	}
	if _, err := os.Stat(helperTarget + ".old"); err != nil {
		t.Errorf("helper backup missing (stat %v) — the replace must go through "+
			"swapOne's rename-aside, or a running handler blocks the update on Windows", err)
	}
}

// A pre-helper archive stages without one; the swap must not treat its absence
// as a failure. This is every downgrade, and every release before v1.5x.
func TestSwapPair_NoStagedHelper_StillSucceeds(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quild.exe"), []byte("new-quild"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, manifestDeclaring("quil.exe", "quild.exe"), "windows"); err != nil {
		t.Fatalf("swapPair() with no staged helper = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "quil-activate.exe")); !os.IsNotExist(err) {
		t.Errorf("quil-activate.exe materialised from nothing, stat err = %v", err)
	}
}

// THE LOAD-BEARING ONE. The helper is a convenience — it removes a console
// flash — while the pair swap is the update itself. A helper that cannot be
// written (pinned by a running click handler, denied by antivirus, out of disk)
// must not fail an update whose binaries already landed, and must certainly not
// roll them back: the version gate would then see a matched old pair and the
// user would be told the update failed when nothing was wrong with it.
func TestSwapPair_HelperInstallFails_PairStillInstalled(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil.exe")
	quildTarget := filepath.Join(installDir, "quild.exe")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil.exe"), []byte("new-quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quild.exe"), []byte("new-quild"), 0755)
	// A DIRECTORY where the staged helper should be: it passes the existence
	// probe and then fails the copy, which is the shape of every real failure
	// here (the file is there, writing it does not work).
	os.Mkdir(filepath.Join(stagedDir, "quil-activate.exe"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, manifestDeclaring("quil.exe", "quild.exe", "quil-activate.exe"), "windows"); err != nil {
		t.Fatalf("swapPair() = %v, want nil — a failed helper install must not fail the update", err)
	}
	gotQuil, err := os.ReadFile(quilTarget)
	if err != nil || string(gotQuil) != "new-quil" {
		t.Errorf("quil after failed helper install = %q (err %v), want new-quil", gotQuil, err)
	}
	gotQuild, err := os.ReadFile(quildTarget)
	if err != nil || string(gotQuild) != "new-quild" {
		t.Errorf("quild after failed helper install = %q (err %v), want new-quild", gotQuild, err)
	}

	// And it must leave NO partial helper behind. copyFile opens
	// O_CREATE|O_TRUNC before io.Copy, so a failure mid-transfer would strand a
	// zero-byte executable — and activatecmd.go admits any non-directory, so
	// `notify setup` would register that empty file as the quil:// handler.
	// Every later click then dies inside CreateProcess with no UI: a DEAD
	// handler, which is strictly worse than the working `quil.exe activate`
	// fallback this path promises when no helper is present.
	if _, err := os.Stat(filepath.Join(installDir, "quil-activate.exe")); !os.IsNotExist(err) {
		t.Errorf("a partial helper survived a failed install (stat err = %v) — "+
			"notify setup would register it and every toast click would silently do nothing", err)
	}
}

// Nothing optional exists off Windows, and a stray file named like the helper
// in a Unix archive must not be installed on the strength of its name.
func TestSwapPair_UnixGoos_IgnoresHelper(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()

	quilTarget := filepath.Join(installDir, "quil")
	quildTarget := filepath.Join(installDir, "quild")
	os.WriteFile(quilTarget, []byte("old-quil"), 0755)
	os.WriteFile(quildTarget, []byte("old-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil"), []byte("new-quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quild"), []byte("new-quild"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("new-activate"), 0755)

	if err := swapPair(quilTarget, quildTarget, stagedDir, manifestDeclaring("quil", "quild"), "linux"); err != nil {
		t.Fatalf("swapPair() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "quil-activate.exe")); !os.IsNotExist(err) {
		t.Errorf("helper installed on linux, stat err = %v", err)
	}
}

// writeHelperBackups litters every backup slot shape removeBackups is meant to
// clear: the canonical ".old", the first numbered fallback, and a gap-separated
// higher one (removeBackups is gap-tolerant by design — slot 1 can outlive
// slot 2 when a process pins one of them).
func writeHelperBackups(t *testing.T, target string) {
	t.Helper()
	os.WriteFile(target+".old", []byte("stale"), 0755)
	os.WriteFile(target+".old.1", []byte("stale"), 0755)
	os.WriteFile(target+".old.7", []byte("stale"), 0755)
}

// The backups installOptional leaves must actually get swept.
//
// Untested until now, and structurally hard to reach: cleanupAppliedUpdate
// takes no arguments and resolves config.UpdateDir() and os.Executable()
// itself, so the sweep could only be verified by reading it. Extracting
// sweepOptionalBackups is what makes this assertable — a helper backup that is
// never cleared is the same accumulating-leftover class freeBackupPath's
// numbered slots exist to route around, except nothing would ever route around
// THIS one, since it is not on the path any later swap probes.
func TestSweepOptionalBackups_RemovesHelperBackups(t *testing.T) {
	installDir := t.TempDir()
	exe := filepath.Join(installDir, "quil.exe")
	helper := filepath.Join(installDir, "quil-activate.exe")
	os.WriteFile(exe, []byte("quil"), 0755)
	os.WriteFile(helper, []byte("helper"), 0755)
	writeHelperBackups(t, helper)

	sweepOptionalBackups(exe, "windows")

	for _, suffix := range []string{".old", ".old.1", ".old.7"} {
		if _, err := os.Stat(helper + suffix); !os.IsNotExist(err) {
			t.Errorf("helper backup %s survived the sweep, stat err = %v", suffix, err)
		}
	}
	// The live helper itself is NOT a backup and must survive — removeBackups
	// builds its paths by construction rather than globbing, and a glob over
	// an install path containing "[" or "*" is exactly how that distinction
	// gets lost.
	if got, err := os.ReadFile(helper); err != nil || string(got) != "helper" {
		t.Errorf("helper itself = %q (err %v), want it untouched by a BACKUP sweep", got, err)
	}
}

// Nothing optional exists off Windows, so the sweep must touch nothing there —
// including a file that merely happens to be named like a helper backup.
func TestSweepOptionalBackups_UnixGoos_TouchesNothing(t *testing.T) {
	installDir := t.TempDir()
	exe := filepath.Join(installDir, "quil")
	decoy := filepath.Join(installDir, "quil-activate.exe.old")
	os.WriteFile(exe, []byte("quil"), 0755)
	os.WriteFile(decoy, []byte("not ours"), 0755)

	sweepOptionalBackups(exe, "linux")

	if got, err := os.ReadFile(decoy); err != nil || string(got) != "not ours" {
		t.Errorf("decoy = %q (err %v), want it untouched on a platform with no optional tier", got, err)
	}
}

// A stat that fails for a reason OTHER than not-exists must not be read as
// "the target is absent".
//
// Guessing absent selects copyFile, which cannot work against a Windows helper
// some process still runs as its image — and that is the very condition most
// likely to make the stat fail in the first place, so the wrong guess lands
// exactly when it does the most harm.
//
// Both cases are driven through the statTarget seam because neither arm is
// reachable otherwise: a real fixture cannot make os.Stat return a denied ACL,
// and putting a directory in the target's place makes os.Stat SUCCEED, so it
// exercises neither branch. The assertion is which FILESYSTEM EFFECT follows,
// not which line ran: with the target genuinely absent on disk, the fresh-copy
// path creates it and reports success, while the swapOne path cannot rename a
// file that is not there and reports failure. The two outcomes are opposite,
// so neither can be mistaken for the other.
func TestInstallOptional_StatFailsNotNotExist_DoesNotTakeFreshCopyPath(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()
	quilTarget := filepath.Join(installDir, "quil.exe")
	os.WriteFile(quilTarget, []byte("quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("new-activate"), 0755)

	orig := statTarget
	t.Cleanup(func() { statTarget = orig })
	statTarget = func(string) (os.FileInfo, error) {
		return nil, fmt.Errorf("stat %s: %w", "quil-activate.exe", os.ErrPermission)
	}

	err := installOptional(quilTarget, stagedDir, manifestDeclaring("quil-activate.exe"), "windows")
	if err == nil {
		t.Error("installOptional() = nil — an unreadable target was treated as absent " +
			"and copied over, which is the branch that fails on a running Windows helper")
	}
	if _, statErr := os.Stat(filepath.Join(installDir, "quil-activate.exe")); statErr == nil {
		t.Error("helper was created via the fresh-copy path despite an ambiguous stat; " +
			"an unreadable target must go through swapOne's rename-aside instead")
	}
}

// The mirror case, so the guard above cannot be satisfied by refusing every
// stat error: a genuine ErrNotExist MUST still take the fresh-copy path, which
// is the ordinary case for an install that has only ever been upgraded.
func TestInstallOptional_StatIsNotExist_TakesFreshCopyPath(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()
	quilTarget := filepath.Join(installDir, "quil.exe")
	os.WriteFile(quilTarget, []byte("quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("new-activate"), 0755)

	orig := statTarget
	t.Cleanup(func() { statTarget = orig })
	statTarget = func(name string) (os.FileInfo, error) {
		return nil, fmt.Errorf("stat %s: %w", name, fs.ErrNotExist)
	}

	if err := installOptional(quilTarget, stagedDir, manifestDeclaring("quil-activate.exe"), "windows"); err != nil {
		t.Fatalf("installOptional() = %v, want nil", err)
	}
	got, err := os.ReadFile(filepath.Join(installDir, "quil-activate.exe"))
	if err != nil || string(got) != "new-activate" {
		t.Errorf("helper = %q (err %v), want new-activate installed by the fresh-copy path", got, err)
	}
}

// manifestDeclaring builds the manifest a verified stage would have produced
// for exactly these names.
//
// The hashes are deliberately junk: installOptional gates on DECLARATION, not
// on content, because by the time it runs VerifyStaged has already re-hashed
// every declared file and refused the whole apply otherwise. A fixture carrying
// real digests would imply this function re-checks them, which it does not and
// must not — doing the hashing twice in two places is how the two copies drift.
func manifestDeclaring(names ...string) *update.Manifest {
	files := make(map[string]string, len(names))
	for _, n := range names {
		files[n] = "not-checked-here"
	}
	return &update.Manifest{Version: "9.9.9", Files: files}
}

// A staged helper the manifest does not declare must NOT be installed.
//
// This is the attack the manifest gate exists for, and it is cheap: an attacker
// who can write into <QUIL_HOME>/update/staged/<ver>/ drops byte-copies of the
// real quil.exe and quild.exe, a manifest declaring only those two, and an
// undeclared quil-activate.exe of their choosing. VerifyStaged passes — the
// manifest covers both required names and both hash correctly — and it never
// enumerates the directory, so the extra file is not merely unverified, it is
// unseen. Installing on presence alone would then place that payload beside
// quil.exe, where `notify setup` registers it as the quil:// handler, and where
// an install that ALREADY has a helper simply has its registered handler
// overwritten in place: the registry entry still names the same path and still
// looks correct, and the next toast click runs the payload.
//
// VerifyStaged's own comment records that this project already fought this
// exact fight once — "the manifest is attacker-controlled the moment anything
// can write into the staged dir" — for quild. This pins it for the third binary.
func TestInstallOptional_UndeclaredStagedHelper_NotInstalled(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()
	quilTarget := filepath.Join(installDir, "quil.exe")
	os.WriteFile(quilTarget, []byte("quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("PAYLOAD"), 0755)

	// The manifest a verified-but-hostile stage would carry: the required pair
	// and nothing else.
	man := manifestDeclaring("quil.exe", "quild.exe")

	if err := installOptional(quilTarget, stagedDir, man, "windows"); err != nil {
		t.Fatalf("installOptional() = %v, want nil — an undeclared file is skipped, not an error", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "quil-activate.exe")); !os.IsNotExist(err) {
		t.Fatal("an UNDECLARED staged helper was installed — it was never hashed by " +
			"VerifyStaged, and notify setup would register it as the quil:// handler")
	}
}

// The same gate must hold for the REPLACEMENT path, which is the worse half:
// there, an existing correctly-registered helper is overwritten in place, so the
// payload inherits a registry entry that was never rewritten.
func TestInstallOptional_UndeclaredStagedHelper_DoesNotOverwriteExisting(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()
	quilTarget := filepath.Join(installDir, "quil.exe")
	helper := filepath.Join(installDir, "quil-activate.exe")
	os.WriteFile(quilTarget, []byte("quil"), 0755)
	os.WriteFile(helper, []byte("legitimate-helper"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("PAYLOAD"), 0755)

	if err := installOptional(quilTarget, stagedDir, manifestDeclaring("quil.exe", "quild.exe"), "windows"); err != nil {
		t.Fatalf("installOptional() = %v, want nil", err)
	}
	got, err := os.ReadFile(helper)
	if err != nil || string(got) != "legitimate-helper" {
		t.Fatalf("registered helper = %q (err %v) — an undeclared staged file replaced the "+
			"real handler in place, inheriting its registry entry", got, err)
	}
}

// A nil manifest installs nothing. No manifest is no verification, and the
// fallback must be to skip rather than to trust the directory.
func TestInstallOptional_NilManifest_InstallsNothing(t *testing.T) {
	installDir := t.TempDir()
	stagedDir := t.TempDir()
	quilTarget := filepath.Join(installDir, "quil.exe")
	os.WriteFile(quilTarget, []byte("quil"), 0755)
	os.WriteFile(filepath.Join(stagedDir, "quil-activate.exe"), []byte("PAYLOAD"), 0755)

	if err := installOptional(quilTarget, stagedDir, nil, "windows"); err != nil {
		t.Fatalf("installOptional() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "quil-activate.exe")); !os.IsNotExist(err) {
		t.Error("a nil manifest installed a helper — unverified by construction")
	}
}
