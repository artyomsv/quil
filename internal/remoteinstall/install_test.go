package remoteinstall

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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
	r := &fakeRunner{stdout: probeOut("/home/a", "Linux", "x86_64", "-", "-")}
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

// elfHeader builds a minimal ELF header carrying a given machine type.
// e_machine is a 2-byte field at 0x12 in the byte order named by EI_DATA.
func elfHeader(machine uint16, littleEndian bool) []byte {
	b := make([]byte, 0x40)
	copy(b, "\x7fELF")
	b[4] = 2 // 64-bit
	if littleEndian {
		b[5] = 1
		binary.LittleEndian.PutUint16(b[0x12:0x14], machine)
	} else {
		b[5] = 2
		binary.BigEndian.PutUint16(b[0x12:0x14], machine)
	}
	return b
}

// machoHeader builds a minimal 64-bit little-endian Mach-O header.
func machoHeader(cpuType uint32) []byte {
	b := make([]byte, 32)
	copy(b, []byte{0xcf, 0xfa, 0xed, 0xfe})
	binary.LittleEndian.PutUint32(b[4:8], cpuType)
	return b
}

func TestBinaryArch(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		wantArch string
		wantOK   bool
	}{
		{"elf amd64", elfHeader(elfMachineAMD64, true), "amd64", true},
		{"elf arm64", elfHeader(elfMachineARM64, true), "arm64", true},
		{"elf big-endian arm64", elfHeader(elfMachineARM64, false), "arm64", true},
		{"mach-o amd64", machoHeader(machoCPUTypeAMD64), "amd64", true},
		{"mach-o arm64", machoHeader(machoCPUTypeARM64), "arm64", true},

		// Unknown or undeterminable architectures pass rather than fail: this
		// is a guard against an obvious mistake, not an allowlist.
		{"elf 32-bit arm", elfHeader(0x28, true), "", false},
		{"truncated elf", []byte("\x7fELF"), "", false},
		{"universal mach-o carries several", []byte{0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 2}, "", false},
		{"not an executable", []byte("#!/bin/sh\n"), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arch, ok := binaryArch(tt.body)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && arch != tt.wantArch {
				t.Errorf("arch = %q, want %q", arch, tt.wantArch)
			}
		})
	}
}

// Same-OS wrong-arch binaries are the --from-dir mistake that format checking
// alone misses: both are ELF, both install, and neither can exec. The far-side
// symptom is exit 127 — indistinguishable from "not installed" — so it presents
// as a loop rather than a failure.
func TestPackDir_RejectsWrongArchitecture(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		platform Platform
		wantMsg  string
	}{
		{"amd64 elf for an arm64 host", elfHeader(elfMachineAMD64, true), Platform{"linux", "arm64"}, "amd64"},
		{"arm64 elf for an amd64 host", elfHeader(elfMachineARM64, true), Platform{"linux", "amd64"}, "arm64"},
		{"amd64 mach-o for an arm64 mac", machoHeader(machoCPUTypeAMD64), Platform{"darwin", "arm64"}, "amd64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFakeBinaries(t, dir, tt.body)
			_, err := PackDir(dir, tt.platform)
			if err == nil {
				t.Fatal("error = nil, want rejection")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not name the built-for architecture %q", err, tt.wantMsg)
			}
		})
	}
}

func TestPackDir_AcceptsMatchingArchitecture(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		platform Platform
	}{
		{"elf amd64", elfHeader(elfMachineAMD64, true), Platform{"linux", "amd64"}},
		{"elf arm64", elfHeader(elfMachineARM64, true), Platform{"linux", "arm64"}},
		{"mach-o arm64", machoHeader(machoCPUTypeARM64), Platform{"darwin", "arm64"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFakeBinaries(t, dir, tt.body)
			if _, err := PackDir(dir, tt.platform); err != nil {
				t.Errorf("PackDir error = %v", err)
			}
		})
	}
}

// `quil daemon stop` exits 1 both when the stop failed AND when no daemon was
// running. Propagating non-zero would abort every upgrade of an idle host;
// swallowing it hides a daemon that kept serving the old binary. So the benign
// case is classified by the marker our own CLI prints, and everything else is
// surfaced as a warning.
func TestStopRemoteDaemon_ClassifiesTheOutcome(t *testing.T) {
	tests := []struct {
		name        string
		exitCode    int
		output      string
		wantWarning bool
	}{
		{"stopped cleanly", 0, "daemon stopped", false},
		{"nothing was running", 1, "environment: production\ndaemon not running", false},
		{"stop genuinely failed", 1, "stop daemon: daemon did not exit within 5s", true},
		{"silent non-zero exit", 3, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{exitCode: tt.exitCode, stdout: tt.output}
			warning, err := StopRemoteDaemon(context.Background(), r, "/usr/local/bin/quil")
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if (warning != "") != tt.wantWarning {
				t.Errorf("warning = %q, wantWarning %v", warning, tt.wantWarning)
			}
		})
	}
}

// --from-dir is the only path a dev build has, and this repo's own
// `scripts/dev.sh cross` writes dist/quil-linux-arm64 rather than dist/quil.
// Requiring a manual rename first would make the documented workflow fail on
// its first use.
func TestPackDir_AcceptsCrossBuildNaming(t *testing.T) {
	dir := t.TempDir()
	p := Platform{"linux", "arm64"}
	for _, name := range binaryNames {
		crossName := fmt.Sprintf("%s-%s-%s", name, p.GOOS, p.GOARCH)
		if err := os.WriteFile(filepath.Join(dir, crossName), elfHeader(elfMachineARM64, true), 0o755); err != nil {
			t.Fatalf("write %s: %v", crossName, err)
		}
	}

	src, err := PackDir(dir, p)
	if err != nil {
		t.Fatalf("PackDir error = %v", err)
	}
	if len(src.Archive) == 0 {
		t.Error("archive is empty")
	}
}

// The plain name wins when both layouts are present, so an unpacked release
// archive is never shadowed by a stale cross-build sitting beside it.
func TestPackDir_PrefersThePlainName(t *testing.T) {
	dir := t.TempDir()
	p := Platform{"linux", "amd64"}
	for _, name := range binaryNames {
		if err := os.WriteFile(filepath.Join(dir, name), elfHeader(elfMachineAMD64, true), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		// A wrong-arch cross-build beside it: chosen, this would be rejected.
		cross := fmt.Sprintf("%s-%s-%s", name, p.GOOS, p.GOARCH)
		if err := os.WriteFile(filepath.Join(dir, cross), elfHeader(elfMachineARM64, true), 0o755); err != nil {
			t.Fatalf("write %s: %v", cross, err)
		}
	}
	if _, err := PackDir(dir, p); err != nil {
		t.Errorf("PackDir error = %v, want the plain name to win", err)
	}
}

// The error has to name both layouts, or a user whose directory has neither
// cannot tell what was expected.
func TestPackDir_MissingBinaryNamesBothLayouts(t *testing.T) {
	_, err := PackDir(t.TempDir(), Platform{"linux", "amd64"})
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	for _, want := range []string{"quil", "quil-linux-amd64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Remote output is unbounded and attacker-influenced; the setup timeout is
// otherwise the only limit, which at ssh throughput is gigabytes.
func TestCapWriter_StopsAtTheLimit(t *testing.T) {
	w := &capWriter{limit: 16}
	n, err := w.Write([]byte(strings.Repeat("A", 1000)))
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	// A short count would make io.Copy and exec's copier treat capping as a
	// write error and tear the command down.
	if n != 1000 {
		t.Errorf("n = %d, want the full input length 1000", n)
	}
	if got := len(w.String()); got != 16 {
		t.Errorf("buffered %d bytes, want 16", got)
	}
}
