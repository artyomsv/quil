package remoteinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records what it was asked to run and replays a canned result.
type fakeRunner struct {
	gotCommands []string
	gotStdin    []byte
	stdout      string
	stderr      string
	exitCode    int
	err         error
}

func (f *fakeRunner) Run(_ context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	f.gotCommands = append(f.gotCommands, command)
	if stdin != nil {
		f.gotStdin, _ = io.ReadAll(stdin)
	}
	if stdout != nil {
		io.WriteString(stdout, f.stdout)
	}
	if stderr != nil {
		io.WriteString(stderr, f.stderr)
	}
	return f.exitCode, f.err
}

func TestRunProbe_ParsesRemoteReport(t *testing.T) {
	r := &fakeRunner{stdout: "/home/a\nLinux\nx86_64\n-\n-\n"}
	got, err := RunProbe(context.Background(), r)
	if err != nil {
		t.Fatalf("RunProbe error = %v", err)
	}
	if got.Platform != (Platform{"linux", "amd64"}) {
		t.Errorf("Platform = %+v", got.Platform)
	}
	// The probe travels on stdin, so the command is the bare `sh -s`: nothing
	// is interpolated and nothing needs quoting.
	if len(r.gotCommands) != 1 || r.gotCommands[0] != "sh -s" {
		t.Errorf("commands = %q, want [\"sh -s\"]", r.gotCommands)
	}
	if !strings.Contains(string(r.gotStdin), "uname -s") {
		t.Error("probe script was not delivered on stdin")
	}
}

// The probe script always exits 0, so a non-zero status came from ssh. Blaming
// the probe would send the user to inspect the wrong machine.
func TestRunProbe_NonZeroExitIsReportedAsSSHFailure(t *testing.T) {
	r := &fakeRunner{exitCode: 255, stderr: "Permission denied (publickey)."}
	_, err := RunProbe(context.Background(), r)
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if !strings.Contains(err.Error(), "ssh exited 255") {
		t.Errorf("error %q does not attribute the failure to ssh", err)
	}
	if !strings.Contains(err.Error(), "publickey") {
		t.Errorf("error %q drops ssh's own explanation", err)
	}
}

func TestRunProbe_RunnerErrorIsWrapped(t *testing.T) {
	r := &fakeRunner{err: errors.New("ssh binary missing")}
	if _, err := RunProbe(context.Background(), r); err == nil {
		t.Fatal("error = nil, want error")
	}
}

func TestInstallCommand_QuotesEveryInterpolatedValue(t *testing.T) {
	cmd := InstallCommand(Target{Dir: "/home/o'brien/.local/bin"}, Source{SHA256: "abc123"})

	if !strings.HasPrefix(cmd, "sh -c '") {
		t.Errorf("script is not passed to sh -c: %q", cmd[:min(60, len(cmd))])
	}
	if !strings.Contains(cmd, `'\''brien`) {
		t.Error("target directory was not single-quote escaped")
	}
	if !strings.Contains(cmd, "'abc123'") {
		t.Error("hash was not quoted")
	}
	// $0 must sit between the script and the arguments, or the target
	// directory becomes $0 and the script reads the hash as $1.
	if !strings.Contains(cmd, " "+scriptArg0+" ") {
		t.Errorf("missing $0 placeholder between script and arguments: %q", cmd)
	}
}

func TestDaemonStopCommand_QuotesThePath(t *testing.T) {
	got := DaemonStopCommand("/home/o'brien/.local/bin/quil")
	if !strings.Contains(got, `'\''brien`) {
		t.Errorf("binary path was not escaped: %q", got)
	}
	if !strings.HasSuffix(got, " daemon stop") {
		t.Errorf("got %q, want it to end with ` daemon stop`", got)
	}
}

func TestPush_SendsArchiveOnStdin(t *testing.T) {
	r := &fakeRunner{}
	src := Source{Archive: []byte("ARCHIVE-BYTES"), SHA256: "deadbeef"}
	if err := Push(context.Background(), r, Target{Dir: "/opt/bin"}, src); err != nil {
		t.Fatalf("Push error = %v", err)
	}
	if string(r.gotStdin) != "ARCHIVE-BYTES" {
		t.Errorf("stdin = %q, want the archive bytes", r.gotStdin)
	}
}

func TestPush_ReportsRemoteFailure(t *testing.T) {
	// Exit 2 is the install script's checksum-mismatch status.
	r := &fakeRunner{exitCode: 2, stderr: "quil-install: archive checksum mismatch (transfer corrupted)"}
	err := Push(context.Background(), r, Target{Dir: "/opt/bin"}, Source{SHA256: "x"})
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error %q drops the installer's explanation", err)
	}
}

// Remote output reaches the operator's terminal, so it must not be able to
// carry escape sequences or unbounded text into an error line.
func TestPush_SanitizesAndBoundsRemoteOutput(t *testing.T) {
	r := &fakeRunner{exitCode: 1, stderr: "\x1b]52;c;cGF3bmVk\x07evil\x1b[2J" + strings.Repeat("A", 5000)}
	err := Push(context.Background(), r, Target{Dir: "/opt/bin"}, Source{})
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\x1b\x07") {
		t.Error("error message carries terminal control characters")
	}
	if len(msg) > 1000 {
		t.Errorf("error message is %d bytes; remote output was not bounded", len(msg))
	}
}

func TestPush_SilentNonZeroExitStillExplains(t *testing.T) {
	r := &fakeRunner{exitCode: 3}
	err := Push(context.Background(), r, Target{Dir: "/opt/bin"}, Source{})
	if err == nil || !strings.Contains(err.Error(), "3") {
		t.Errorf("error = %v, want it to name the exit status", err)
	}
}

// A daemon that was not running makes `quil daemon stop` exit non-zero, and
// that is the state an upgrade wants — it must not abort the install.
func TestStopRemoteDaemon_ToleratesNonZeroExit(t *testing.T) {
	r := &fakeRunner{exitCode: 1, stderr: "no daemon running"}
	if err := StopRemoteDaemon(context.Background(), r, "/usr/local/bin/quil"); err != nil {
		t.Errorf("error = %v, want nil for a daemon that was not running", err)
	}
}

func writeFakeBinaries(t *testing.T, dir string, magic []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range binaryNames {
		body := append(append([]byte{}, magic...), []byte("-"+name)...)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestPackDir_ProducesAVerifiableArchive(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinaries(t, dir, []byte("\x7fELF"))

	src, err := PackDir(dir, Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("PackDir error = %v", err)
	}
	if len(src.Archive) == 0 {
		t.Fatal("archive is empty")
	}
	if len(src.SHA256) != 64 {
		t.Errorf("SHA256 = %q, want 64 hex characters", src.SHA256)
	}
}

func TestPackDir_RejectsMissingBinary(t *testing.T) {
	dir := t.TempDir() // holds neither quil nor quild
	_, err := PackDir(dir, Platform{"linux", "amd64"})
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if !strings.Contains(err.Error(), "quil") {
		t.Errorf("error %q does not name the missing binary", err)
	}
}

// --from-dir invites exactly one mistake: pointing it at this machine's build
// output. Catching it here beats discovering it as exit 127 on the far side,
// which is indistinguishable from "not installed" and would loop.
func TestPackDir_RejectsWrongExecutableFormat(t *testing.T) {
	tests := []struct {
		name     string
		magic    []byte
		platform Platform
		wantMsg  string
	}{
		{"windows binaries for a linux host", []byte("MZ\x90\x00"), Platform{"linux", "amd64"}, "Windows"},
		{"mach-o binaries for a linux host", []byte{0xcf, 0xfa, 0xed, 0xfe}, Platform{"linux", "amd64"}, "ELF"},
		{"elf binaries for a mac", []byte("\x7fELF"), Platform{"darwin", "arm64"}, "Mach-O"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFakeBinaries(t, dir, tt.magic)
			_, err := PackDir(dir, tt.platform)
			if err == nil {
				t.Fatal("error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestPackDir_AcceptsEachSupportedFormat(t *testing.T) {
	tests := []struct {
		name     string
		magic    []byte
		platform Platform
	}{
		{"elf on linux", []byte("\x7fELF"), Platform{"linux", "amd64"}},
		{"mach-o 64 little endian", []byte{0xcf, 0xfa, 0xed, 0xfe}, Platform{"darwin", "arm64"}},
		{"universal binary", []byte{0xca, 0xfe, 0xba, 0xbe}, Platform{"darwin", "amd64"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFakeBinaries(t, dir, tt.magic)
			if _, err := PackDir(dir, tt.platform); err != nil {
				t.Errorf("PackDir error = %v", err)
			}
		})
	}
}
