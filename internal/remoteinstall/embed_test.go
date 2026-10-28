package remoteinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The scripts are go:embed'ed and executed by a POSIX shell. A CRLF checkout on
// a Windows build host bakes CR into the binary, and POSIX shells do not treat
// CR as whitespace: `fi\r` is not the `fi` keyword, so an if-block never closes
// and the parse dies at the next construct it cannot absorb.
//
// This exact bug shipped from this repo before — a CRLF bash-init.sh embedded
// into a Windows-built daemon broke shell integration and PATH on every Linux
// host it ran on — and it is invisible when developing on Windows, because the
// working tree is CRLF there by design. .gitattributes carries `*.sh text
// eol=lf`; this asserts it actually held all the way into the binary.
func TestEmbeddedScripts_AreLF(t *testing.T) {
	for name, body := range embeddedScripts() {
		t.Run(name, func(t *testing.T) {
			if body == "" {
				t.Fatal("embedded script is empty — check the go:embed path")
			}
			if i := strings.IndexByte(body, '\r'); i >= 0 {
				t.Errorf("carriage return at byte %d: .gitattributes eol=lf did not hold", i)
			}
		})
	}
}

// Both scripts are run by handing their text to `sh`, never by exec'ing a file,
// so a shebang would be dead weight — and `#!/bin/bash` would be an outright
// portability bug on a host without bash.
func TestEmbeddedScripts_HaveNoShebang(t *testing.T) {
	for name, body := range embeddedScripts() {
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(body, "#!") {
				t.Error("script starts with a shebang; it is passed to sh, not exec'd")
			}
		})
	}
}

func embeddedScripts() map[string]string {
	return map[string]string{
		"remote-probe.sh":     probeScript,
		"remote-stage.sh":     stageScript,
		"remote-commit.sh":    commitScript,
		"remote-probe.ps1":    windowsProbeScript,
		"remote-prepare.ps1":  windowsPrepareScript,
		"remote-finalize.ps1": windowsFinalizeScript,
	}
}

// posixShell skips the test when there is no POSIX shell to run. The scripts
// only ever execute on the remote host, which is Linux or macOS by
// construction; this lets the suite still run natively on a Windows host.
func posixShell(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell available")
	}
	return sh
}

// runProbe executes the probe script with a controlled HOME and PATH, and
// returns its raw stdout.
func runProbe(t *testing.T, home, pathEnv string) string {
	t.Helper()
	cmd := exec.Command(posixShell(t), "-s")
	cmd.Stdin = strings.NewReader(probeScript)
	cmd.Env = []string{"HOME=" + home, "PATH=" + pathEnv}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("probe script failed: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
}

// The probe's five-line contract and ParseProbe are two halves of one
// agreement that otherwise only meet on a real remote host. This is the test
// that makes them meet in CI.
func TestProbeScript_OutputSatisfiesParseProbe(t *testing.T) {
	home := t.TempDir()
	got, err := ParseProbe(runProbe(t, home, os.Getenv("PATH")))
	if err != nil {
		t.Fatalf("ParseProbe rejected the probe script's own output: %v", err)
	}
	if got.Home != home {
		t.Errorf("Home = %q, want %q", got.Home, home)
	}
	// The platform is whatever the test host is; asserting a specific value
	// would pin the test to one CI architecture. That it parsed at all is the
	// contract being checked.
	if got.Platform.GOOS == "" || got.Platform.GOARCH == "" {
		t.Errorf("Platform not populated: %+v", got.Platform)
	}
}

// The whole feature exists because `ssh host quil --stdio` runs a
// non-interactive shell that usually cannot see ~/.local/bin. The probe must
// therefore find an install there with PATH deliberately excluding it.
func TestProbeScript_FindsUserInstallWithoutPATH(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	quil := filepath.Join(binDir, "quil")
	if err := os.WriteFile(quil, []byte("#placeholder\n"), 0o755); err != nil {
		t.Fatalf("write quil: %v", err)
	}

	// PATH excludes binDir entirely — exactly the situation on the remote.
	got, err := ParseProbe(runProbe(t, home, "/usr/bin:/bin"))
	if err != nil {
		t.Fatalf("ParseProbe: %v", err)
	}
	if got.ExistingPath != quil {
		t.Errorf("ExistingPath = %q, want %q (PATH lookup alone would miss it)", got.ExistingPath, quil)
	}
	if !got.ExistingDirWritable {
		t.Error("ExistingDirWritable = false for a directory the test just created")
	}
}

func TestProbeScript_ReportsNoInstallOnAFreshHost(t *testing.T) {
	// A realistic PATH that simply has no quil on it — uname must still
	// resolve, or this would be testing a broken host rather than a fresh one.
	got, err := ParseProbe(runProbe(t, t.TempDir(), "/usr/bin:/bin"))
	if err != nil {
		t.Fatalf("ParseProbe: %v", err)
	}
	if got.ExistingPath != "" {
		t.Errorf("ExistingPath = %q, want empty on a host with no quil", got.ExistingPath)
	}
}

// A host so restricted that uname does not resolve must be refused, not guessed
// at. The script reports "-" rather than failing, so the refusal is the
// parser's job — and getting it wrong would mean choosing an architecture for a
// host that never told us one.
func TestProbeScript_UnresolvableUnameIsRefused(t *testing.T) {
	out := runProbe(t, t.TempDir(), "/nonexistent")
	if _, err := ParseProbe(out); err == nil {
		t.Errorf("ParseProbe accepted a probe that could not run uname: %q", out)
	}
}

// The probe must always exit 0. Its exit code is how the caller tells an ssh
// failure from a probe finding, so a probe that failed to find anything and a
// connection that never happened must not look alike.
func TestProbeScript_AlwaysExitsZero(t *testing.T) {
	cmd := exec.Command(posixShell(t), "-s")
	cmd.Stdin = strings.NewReader(probeScript)
	cmd.Env = []string{"HOME=/nonexistent-home", "PATH=/nonexistent"}
	if err := cmd.Run(); err != nil {
		t.Errorf("probe exited non-zero on a host where nothing resolves: %v", err)
	}
}

// buildArchive produces a tar.gz shaped like a quil release archive and its
// sha256, so the install script can be exercised without a network.
func buildArchive(t *testing.T, contents map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range contents {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// runScript runs one embedded script in the production shape: the script as an
// argument to sh -c, then $0, then its arguments. env adds to the environment.
func runScript(t *testing.T, script string, stdin []byte, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(posixShell(t), append([]string{"-c", script, scriptArg0}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run script: %v", err)
	}
	return code, out.String(), errb.String()
}

// runStage runs remote-stage.sh and returns the staging dir it reported, the
// path production hands to the commit step.
func runStage(t *testing.T, dir string, archive []byte, hash string, env ...string) (code int, staging, out string) {
	t.Helper()
	code, stdout, stderr := runScript(t, stageScript, archive, env, dir, hash)
	if code == 0 {
		var err error
		if staging, err = parseStageOutput(Target{Dir: dir}, stdout); err != nil {
			t.Fatalf("stage succeeded but its report does not parse: %v\n%s", err, stdout)
		}
	}
	return code, staging, stdout + stderr
}

// runInstall runs the whole POSIX install the way runRemoteSetup does: stage,
// then commit what the stage reported.
func runInstall(t *testing.T, dir string, archive []byte, hash string, env ...string) (int, string) {
	t.Helper()
	code, staging, out := runStage(t, dir, archive, hash, env...)
	if code != 0 {
		return code, out
	}
	code, stdout, stderr := runScript(t, commitScript, nil, nil, dir, staging)
	return code, out + stdout + stderr
}

func TestInstallScript_InstallsBothBinaries(t *testing.T) {
	posixShell(t)
	archive, hash := buildArchive(t, map[string]string{
		"quil":  "QUIL-BINARY",
		"quild": "QUILD-BINARY",
	})
	dir := filepath.Join(t.TempDir(), "bin") // deliberately does not exist yet

	code, out := runInstall(t, dir, archive, hash)
	if code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}

	for name, want := range map[string]string{"quil": "QUIL-BINARY", "quild": "QUILD-BINARY"} {
		p := filepath.Join(dir, name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %v)", name, info.Mode().Perm())
		}
	}
}

// The caller already verified this archive against the release checksums before
// sending, so a mismatch here means the transfer was truncated or corrupted.
// Nothing may be written in that case.
func TestInstallScript_RejectsChecksumMismatch(t *testing.T) {
	posixShell(t)
	archive, _ := buildArchive(t, map[string]string{"quil": "X", "quild": "Y"})
	dir := t.TempDir()

	code, out := runInstall(t, dir, archive, strings.Repeat("0", 64))
	if code == 0 {
		t.Fatalf("install succeeded despite a bad hash: %s", out)
	}
	if !strings.Contains(out, "checksum") {
		t.Errorf("output does not explain the failure: %q", out)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Errorf("install wrote %d entries despite a checksum failure", len(entries))
	}
}

// Renaming over a running binary is what makes an upgrade possible: the running
// process keeps its inode. The staging temp files must not survive either.
func TestInstallScript_OverwritesAndLeavesNoTempFiles(t *testing.T) {
	posixShell(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quil"), []byte("OLD"), 0o755); err != nil {
		t.Fatalf("seed old binary: %v", err)
	}

	archive, hash := buildArchive(t, map[string]string{"quil": "NEW", "quild": "NEWD"})
	if code, out := runInstall(t, dir, archive, hash); code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}

	got, err := os.ReadFile(filepath.Join(dir, "quil"))
	if err != nil {
		t.Fatalf("read quil: %v", err)
	}
	if string(got) != "NEW" {
		t.Errorf("quil = %q, want the replacement", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("staging temp file left behind: %s", e.Name())
		}
	}
}

// PackDir's tar layout and the install script's `tar -xzf` are the two halves
// of another agreement that otherwise only meet on a real remote host: a stray
// path prefix or a missing mode would extract to the wrong place, or land
// non-executable, with nothing local to catch it.
func TestInstallScript_AcceptsPackDirOutput(t *testing.T) {
	posixShell(t)
	from := t.TempDir()
	for _, name := range binaryNames {
		body := append([]byte("\x7fELF"), []byte("-"+name)...)
		if err := os.WriteFile(filepath.Join(from, name), body, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	src, err := PackDir(from, Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("PackDir error = %v", err)
	}

	dir := filepath.Join(t.TempDir(), "bin")
	if code, out := runInstall(t, dir, src.Archive, src.SHA256); code != 0 {
		t.Fatalf("install of a PackDir archive exited %d: %s", code, out)
	}
	for _, name := range binaryNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s did not land: %v", name, err)
		}
	}
}

// The install must not stage in $TMPDIR or /tmp. A host whose /tmp tmpfs hit its
// per-user quota failed every upgrade with "cat: write error: Disk quota
// exceeded" while the install directory had plenty of room. A TMPDIR that
// cannot be written stands in for that full /tmp.
func TestInstallScript_DoesNotStageInTmpdir(t *testing.T) {
	posixShell(t)
	archive, hash := buildArchive(t, map[string]string{"quil": "Q", "quild": "D"})
	dir := filepath.Join(t.TempDir(), "bin")

	code, out := runInstall(t, dir, archive, hash, "TMPDIR="+filepath.Join(t.TempDir(), "does-not-exist"))
	if code != 0 {
		t.Fatalf("install with an unusable TMPDIR exited %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "quil")); err != nil {
		t.Errorf("quil not installed: %v", err)
	}
	assertNoDotEntries(t, dir)
}

func assertNoDotEntries(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("staging left behind: %s", e.Name())
		}
	}
}

// The stage step must change nothing the host runs: the daemon is still up
// while it runs, and it is the step expected to fail. The old binaries stay
// exactly as they were until the commit.
func TestStageScript_LeavesTheInstalledBinariesAlone(t *testing.T) {
	posixShell(t)
	dir := t.TempDir()
	for _, n := range []string{"quil", "quild"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("OLD-"+n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	archive, hash := buildArchive(t, map[string]string{"quil": "NEW-quil", "quild": "NEW-quild"})

	code, staging, out := runStage(t, dir, archive, hash)
	if code != 0 {
		t.Fatalf("stage exited %d: %s", code, out)
	}
	for _, n := range []string{"quil", "quild"} {
		if got, _ := os.ReadFile(filepath.Join(dir, n)); string(got) != "OLD-"+n {
			t.Errorf("stage touched the installed %s: %q", n, got)
		}
		if got, _ := os.ReadFile(filepath.Join(staging, n)); string(got) != "NEW-"+n {
			t.Errorf("staged %s = %q, want the new binary", n, got)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, "quil.tar.gz")); err == nil {
		t.Error("the archive was left in the staging dir; it doubles the space the install needs")
	}
}

// An interrupted install leaves a full copy of the binaries behind; the next
// stage clears it rather than letting copies pile up in the install directory.
// Only an ABANDONED one, though: a young staging dir may be another client's
// upgrade in flight, and is kept.
func TestStageScript_ClearsOnlyAbandonedStagingDirs(t *testing.T) {
	posixShell(t)
	dir := t.TempDir()
	stale := filepath.Join(dir, ".quil-staging-OLD123")
	young := filepath.Join(dir, ".quil-staging-NEW123")
	for _, d := range []string{stale, young} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	archive, hash := buildArchive(t, map[string]string{"quil": "Q", "quild": "D"})
	if code, _, out := runStage(t, dir, archive, hash); code != 0 {
		t.Fatalf("stage exited %d: %s", code, out)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("an abandoned staging dir survived the next stage")
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("a young staging dir, possibly another upgrade in flight, was removed: %v", err)
	}
}

// Two clients upgrading one host: A has staged and is stopping the daemon
// when B's stage starts and fails. A's commit must still find its files —
// otherwise A stopped the daemon for nothing, which is the failure staging
// exists to prevent.
func TestStageScript_ASecondStageDoesNotBreakAnUpgradeInFlight(t *testing.T) {
	posixShell(t)
	dir := t.TempDir()
	archive, hash := buildArchive(t, map[string]string{"quil": "A-quil", "quild": "A-quild"})

	codeA, stagingA, out := runStage(t, dir, archive, hash)
	if codeA != 0 {
		t.Fatalf("stage A exited %d: %s", codeA, out)
	}
	if codeB, _, outB := runStage(t, dir, archive, strings.Repeat("0", 64)); codeB == 0 {
		t.Fatalf("stage B with a bad hash succeeded: %s", outB)
	}
	code, stdout, stderr := runScript(t, commitScript, nil, nil, dir, stagingA)
	if code != 0 {
		t.Fatalf("commit A failed after stage B ran: %d %s%s", code, stdout, stderr)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "quil")); string(got) != "A-quil" {
		t.Errorf("quil = %q, want A's", got)
	}
}

// The install directory came from the probe and may contain "..": that is a
// legal name, not a traversal, and the whole install must go through. The
// commit used to refuse it — after the daemon had been stopped.
func TestInstallScript_AcceptsATargetWithConsecutiveDots(t *testing.T) {
	posixShell(t)
	dir := filepath.Join(t.TempDir(), "quil..backup", "bin")
	archive, hash := buildArchive(t, map[string]string{"quil": "Q", "quild": "D"})
	if code, out := runInstall(t, dir, archive, hash); code != 0 {
		t.Fatalf("install into %s exited %d: %s", dir, code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "quil")); string(got) != "Q" {
		t.Errorf("quil = %q, want the new binary", got)
	}
}

// An archive without both binaries is refused at the stage, while the daemon
// is still running, rather than discovered by the commit after it stopped.
func TestStageScript_RefusesAnArchiveMissingABinary(t *testing.T) {
	posixShell(t)
	dir := t.TempDir()
	archive, hash := buildArchive(t, map[string]string{"quil": "Q"})
	code, _, out := runStage(t, dir, archive, hash)
	if code == 0 {
		t.Fatalf("staged an archive with no quild: %s", out)
	}
	if !strings.Contains(out, "quild") {
		t.Errorf("refusal does not name the missing binary: %q", out)
	}
	assertNoDotEntries(t, dir)
}

// The commit's last command is `rm -rf` on its second argument, so the script
// refuses anything but a staging dir directly under the target, even though
// the caller already checked the shape.
//
// Every bad path is a directory that HOLDS quil and quild, so the guard is the
// only thing that can refuse it: the later "staged binaries are missing" check
// would otherwise pass, and without the guard the commit would move the files
// and delete the directory.
func TestCommitScript_RefusesAPathOutsideTheTarget(t *testing.T) {
	posixShell(t)
	base := t.TempDir()
	dir := filepath.Join(base, "bin")
	victim := filepath.Join(base, "victim")
	nested := filepath.Join(dir, ".quil-staging-a", "sub")
	populated := []string{dir, victim, nested, filepath.Join(dir, ".quil-staging-a")}
	for _, d := range populated {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"quil", "quild", "keep"} {
			if err := os.WriteFile(filepath.Join(d, n), []byte(n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	for name, bad := range map[string]string{
		"outside the target": victim,
		"the target itself":  dir,
		"empty suffix":       dir + "/.quil-staging-",
		"traversal":          dir + "/.quil-staging-a/../../victim",
		"nested":             nested,
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runScript(t, commitScript, nil, nil, dir, bad)
			if code != 2 || !strings.Contains(stderr, "is not a staging directory") {
				t.Errorf("commit of %q: exit %d %q, want the guard's refusal", bad, code, stderr)
			}
		})
	}
	for _, d := range populated {
		for _, n := range []string{"quil", "quild", "keep"} {
			if _, err := os.Stat(filepath.Join(d, n)); err != nil {
				t.Errorf("commit touched %s: %v", filepath.Join(d, n), err)
			}
		}
	}
}

// A directory containing an apostrophe is the case ShellSingleQuote exists for,
// and it must survive the whole round trip into the script's $1.
func TestInstallScript_HandlesAwkwardDirectoryNames(t *testing.T) {
	posixShell(t)
	dir := filepath.Join(t.TempDir(), "o'brien's bin")
	archive, hash := buildArchive(t, map[string]string{"quil": "A", "quild": "B"})

	if code, out := runInstall(t, dir, archive, hash); code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "quil")); err != nil {
		t.Errorf("quil not installed into a directory with an apostrophe: %v", err)
	}
}
