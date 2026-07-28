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
		"remote-probe.sh":   probeScript,
		"remote-install.sh": installScript,
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

// runInstall executes the install script in the production shape: the script
// as an argument to sh -c, the archive on stdin.
func runInstall(t *testing.T, dir string, archive []byte, hash string) (int, string) {
	t.Helper()
	cmd := exec.Command(posixShell(t), "-c", installScript, "quil-install", dir, hash)
	cmd.Stdin = bytes.NewReader(archive)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run install script: %v", err)
	}
	return code, stdout.String() + stderr.String()
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
