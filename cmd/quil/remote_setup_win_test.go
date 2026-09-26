package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/remoteinstall"
)

// noSSHRunner stands in for ssh in every test that does not script one: a
// run fails as a missing ssh binary would, rather than reaching a real host.
type noSSHRunner struct{}

func (noSSHRunner) Run(context.Context, string, io.Reader, io.Writer, io.Writer) (int, error) {
	return -1, errors.New("test: no ssh")
}

// runStep is one scripted remote command: the prefix it must start with, and
// the exit code and output it answers with.
type runStep struct {
	prefix string
	code   int
	out    string
}

// stepRunner answers each command with the next step, in order, and fails the
// test on a command out of sequence — the install is an ordered sequence.
type stepRunner struct {
	t     *testing.T
	steps []runStep
	ran   []string
}

func (s *stepRunner) Run(_ context.Context, cmd string, _ io.Reader, stdout, _ io.Writer) (int, error) {
	s.ran = append(s.ran, cmd)
	i := len(s.ran) - 1
	if i >= len(s.steps) {
		s.t.Errorf("unexpected remote command #%d: %.80q", i, cmd)
		return 1, nil
	}
	if !strings.HasPrefix(cmd, s.steps[i].prefix) {
		s.t.Errorf("remote command #%d = %.80q, want prefix %q", i, cmd, s.steps[i].prefix)
	}
	_, _ = io.WriteString(stdout, s.steps[i].out)
	return s.steps[i].code, nil
}

// peImage is a minimal amd64 PE header, enough for PackDir's format check.
func peImage() []byte {
	b := make([]byte, 0x100)
	copy(b, "MZ")
	b[0x3c] = 0x80
	copy(b[0x80:], "PE\x00\x00")
	b[0x84], b[0x85] = 0x64, 0x86
	return b
}

const (
	winLocal   = `C:\Users\a\AppData\Local`
	winDir     = winLocal + `\Programs\quil`
	winQuil    = winDir + `\quil.exe`
	winTar     = `C:\Windows\System32\tar.exe`
	winStaging = winDir + `\.quil-staging-0123456789abcdef0123456789abcdef`
)

func winProbe() remoteinstall.Probe {
	return remoteinstall.Probe{
		OS:       "windows",
		Home:     winLocal,
		Platform: remoteinstall.Platform{GOOS: "windows", GOARCH: "amd64"},
		Shell:    remoteinstall.ShellCmd,
	}
}

func winFromDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"quil.exe", "quild.exe", "quil-activate.exe"} {
		if err := os.WriteFile(filepath.Join(dir, n), peImage(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// winInstallSteps are the three install round trips, prepare → tar → finalize.
func winInstallSteps() []runStep {
	return []runStep{
		{prefix: "powershell.exe ", out: "__quil_prepare__\r\n" + winStaging + "\r\n" + winTar + "\r\n"},
		{prefix: `"` + winTar + `" -xf - -C "` + winStaging + `"`},
		{prefix: "powershell.exe ", out: "__quil_install__\r\n" + winQuil + "\r\n"},
	}
}

// The whole Windows path through the real call site: the install, then the
// logon task registered with the installed binary quoted for the host's
// shell, then the path AND the shell recorded, so the next attach quotes the
// command for cmd rather than for sh.
func TestRunRemoteSetup_Windows_InstallsRegistersLogonTaskAndRecordsShell(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	r := &stepRunner{t: t, steps: append(winInstallSteps(),
		runStep{prefix: `"` + winQuil + `" daemon install-logon`, out: "registered"})}
	setupRunnerFn = func(string) remoteinstall.Runner { return r }

	probe := winProbe()
	var out bytes.Buffer
	err := runRemoteSetup("win01", setupOptions{FromDir: winFromDir(t), Yes: true, Out: &out, probe: &probe})
	if err != nil {
		t.Fatalf("runRemoteSetup: %v\n%s", err, out.String())
	}
	if len(r.ran) != 4 {
		t.Fatalf("ran %d remote commands, want 4 (prepare, tar, finalize, install-logon):\n%q", len(r.ran), r.ran)
	}
	if got := spy.recorded["win01"]; got != winQuil {
		t.Errorf("recorded %q, want %q", got, winQuil)
	}
	if got := spy.shells["win01"]; got != remoteinstall.ShellCmd {
		t.Errorf("recorded shell %q, want %q", got, remoteinstall.ShellCmd)
	}
	if !strings.Contains(out.String(), "Registering the daemon's logon task") {
		t.Errorf("no logon-task narration:\n%s", out.String())
	}
	if strings.Contains(out.String(), "not registered") {
		t.Errorf("reported a failure for a task that registered:\n%s", out.String())
	}
}

// The install already succeeded when the logon task fails, so the failure is
// a warning with the command to run by hand — never an install failure, and
// never a reason to skip recording the path.
func TestRunRemoteSetup_Windows_LogonTaskFailure_WarnsAndStillRecords(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	r := &stepRunner{t: t, steps: append(winInstallSteps(),
		runStep{prefix: `"` + winQuil + `" daemon install-logon`, code: 1,
			out: `install-logon: ` + winDir + `\quil-activate.exe is missing`})}
	setupRunnerFn = func(string) remoteinstall.Runner { return r }

	probe := winProbe()
	var out bytes.Buffer
	if err := runRemoteSetup("win01", setupOptions{FromDir: winFromDir(t), Yes: true, Out: &out, probe: &probe}); err != nil {
		t.Fatalf("a logon-task failure failed the install: %v", err)
	}
	for _, want := range []string{"The logon task was not registered", "quil-activate.exe is missing",
		`Run it on win01 later: "` + winQuil + `" daemon install-logon`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if spy.recorded["win01"] != winQuil {
		t.Errorf("path not recorded after a successful install: %v", spy.recorded)
	}
}

// An upgrade stops the old daemon first, with the command quoted for cmd.
func TestRunRemoteSetup_WindowsUpgrade_StopsDaemonWithHostQuoting(t *testing.T) {
	resetRemoteSetupState(t)
	newHealSpy(t)
	steps := append([]runStep{{prefix: `"` + winQuil + `" daemon stop`}}, winInstallSteps()...)
	r := &stepRunner{t: t, steps: append(steps, runStep{prefix: `"` + winQuil + `" daemon install-logon`})}
	setupRunnerFn = func(string) remoteinstall.Runner { return r }

	probe := winProbe()
	probe.ExistingPath, probe.ExistingDirWritable = winQuil, true
	var out bytes.Buffer
	if err := runRemoteSetup("win01", setupOptions{FromDir: winFromDir(t), Yes: true, Out: &out, probe: &probe}); err != nil {
		t.Fatalf("runRemoteSetup: %v\n%s", err, out.String())
	}
	if len(r.ran) != 5 {
		t.Errorf("ran %q", r.ran)
	}
}

// The control: a POSIX host keeps its single `sh -c` install, runs no logon
// task, and records no shell.
func TestRunRemoteSetup_POSIX_NoLogonTaskAndNoShell(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	r := &stepRunner{t: t, steps: []runStep{{prefix: "sh -c "}}}
	setupRunnerFn = func(string) remoteinstall.Runner { return r }

	dir := t.TempDir()
	elf := make([]byte, 0x40)
	copy(elf, "\x7fELF")
	elf[4], elf[5], elf[0x12] = 2, 1, 0x3e
	for _, n := range []string{"quil", "quild"} {
		if err := os.WriteFile(filepath.Join(dir, n), elf, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probe := remoteinstall.Probe{Home: "/home/a", Platform: remoteinstall.Platform{GOOS: "linux", GOARCH: "amd64"}}
	var out bytes.Buffer
	if err := runRemoteSetup("gpu01", setupOptions{FromDir: dir, Yes: true, Out: &out, probe: &probe}); err != nil {
		t.Fatalf("runRemoteSetup: %v\n%s", err, out.String())
	}
	if len(r.ran) != 1 {
		t.Errorf("ran %d remote commands, want exactly the install: %q", len(r.ran), r.ran)
	}
	if spy.recorded["gpu01"] != "/home/a/.local/bin/quil" || spy.shells["gpu01"] != remoteinstall.ShellPOSIX {
		t.Errorf("recorded %q shell %q", spy.recorded["gpu01"], spy.shells["gpu01"])
	}
}

func TestConfirmRemoteInstall_Windows_NamesTheExeFiles(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = prev; r.Close() })
	if _, err := w.WriteString("n\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	probe := winProbe()
	var ok bool
	out := captureStderr(t, func() {
		ok = confirmRemoteInstall("win01", probe, remoteinstall.PlanTarget(probe), "1.2.3", false)
	})
	if ok {
		t.Error("an answer of n was taken as consent")
	}
	if want := `install to:           ` + winDir + `\{quil,quild}.exe`; !strings.Contains(out, want) {
		t.Errorf("prompt lacks %q:\n%s", want, out)
	}
}

// Exit 1 with quil present at the path we dialled: quil itself refused to
// start. That is not a missing install, and the generic "cannot reach the
// host" text would contradict the quil message printed just above it.
func TestOfferRemoteInstall_Probe_QuilFound_ReportsExitNotInstall(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	isReleaseFn = func() bool { return false } // an install attempt would print "development build"
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{OS: "windows", ExistingPath: `C:\q\quil.exe`}, nil
	}

	var retry bool
	out := captureStderr(t, func() { retry = offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if retry {
		t.Fatal("asked for a re-dial")
	}
	if !remoteFailureReported {
		t.Error("the 'quil exited' message was not flagged as printed")
	}
	if !strings.Contains(out, "exited before its daemon answered") {
		t.Errorf("no explanation printed:\n%s", out)
	}
	if strings.Contains(out, "development build") || strings.Contains(out, "not installed") {
		t.Errorf("offered an install although quil is present:\n%s", out)
	}
	if len(spy.recorded) != 0 || len(spy.cleared) != 0 {
		t.Errorf("record mutated: %v / %v", spy.recorded, spy.cleared)
	}
}

// Windows paths are case-insensitive, and Get-Command may report a different
// case than the planner used — the same path must not read as "elsewhere".
func TestOfferRemoteInstall_Probe_RecordedPathInOtherCase_ReportsExit(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return strings.ToUpper(winQuil) }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		p := winProbe()
		p.ExistingPath, p.ExistingDirWritable = winQuil, true
		return p, nil
	}

	var retry bool
	captureStderr(t, func() { retry = offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if retry || !remoteFailureReported {
		t.Errorf("retry %v reported %v; want the exit reported, no re-dial", retry, remoteFailureReported)
	}
	if len(spy.recorded) != 0 {
		t.Errorf("rewrote a record that names the same file: %v", spy.recorded)
	}
}

// Exit 1 is also what cmd answers when it cannot find the path we dialled, so
// a quil the probe finds ELSEWHERE, in a directory we may adopt, is recorded
// and re-dialled — exactly what healRemoteRecord does for exit 127. Saying
// "quil exited" there would quote cmd's "is not recognized" as quil's own
// message. The re-dial terminates: next time the recorded path IS the one the
// probe reports.
func TestOfferRemoteInstall_Probe_QuilElsewhere_AdoptsAndRetries(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		p := winProbe()
		p.ExistingPath, p.ExistingDirWritable = winQuil, true
		return p, nil
	}

	var retry bool
	captureStderr(t, func() { retry = offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if !retry {
		t.Error("did not ask for a re-dial after correcting the record")
	}
	if remoteFailureReported {
		t.Error("flagged a failure as reported while asking for a re-dial")
	}
	if spy.recorded["win"] != winQuil || spy.shells["win"] != remoteinstall.ShellCmd {
		t.Errorf("recorded %q shell %q", spy.recorded["win"], spy.shells["win"])
	}
}

func TestOfferRemoteInstall_Probe_NoQuil_OffersInstall(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	isReleaseFn = func() bool { return false } // runRemoteSetup stops at plannedVersion
	recordedRemoteBinaryFn = func(string) string { return `C:\old\quil.exe` }
	calls := 0
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		calls++
		return winProbe(), nil
	}

	var retry bool
	out := captureStderr(t, func() { retry = offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if retry {
		t.Error("asked for a re-dial although nothing was installed")
	}
	if calls != 1 || strings.Count(out, "Checking") != 1 {
		t.Errorf("probed %d times, announced %d; want the probe handed to the install, not re-run:\n%s",
			calls, strings.Count(out, "Checking"), out)
	}
	if !strings.Contains(out, "Quil is not installed on win") {
		t.Errorf("no finding printed:\n%s", out)
	}
	// runRemoteSetup was reached WITH the probe: it refused at plannedVersion,
	// which runs after the probe-reuse block.
	if !strings.Contains(out, "development build") {
		t.Errorf("the install offer was not reached:\n%s", out)
	}
	if len(spy.cleared) != 1 || spy.cleared[0] != "win" {
		t.Errorf("stale record not cleared: %v", spy.cleared)
	}
	if remoteFailureReported {
		t.Error("flagged as reported, which would hide the install's own failure")
	}
}

// POSITIVE EVIDENCE ONLY: a probe that failed says nothing, so nothing changes
// and the caller reports the link failure exactly as before.
func TestOfferRemoteInstall_Probe_Error_NoRecordChange(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	isReleaseFn = func() bool { return false }
	recordedRemoteBinaryFn = func(string) string { return winQuil }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{}, errors.New("host down")
	}

	var retry bool
	out := captureStderr(t, func() { retry = offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if retry {
		t.Fatal("re-dial after a failed probe")
	}
	if len(spy.cleared) != 0 || len(spy.recorded) != 0 {
		t.Errorf("record mutated without positive evidence: %v / %v", spy.cleared, spy.recorded)
	}
	if remoteFailureReported {
		t.Error("flagged as reported, which would suppress the link-failure report")
	}
	if strings.Contains(out, "development build") {
		t.Errorf("offered an install on a failed probe:\n%s", out)
	}
}

// healRemoteRecord's termination proof compares the recorded path with the
// probe's; on Windows that comparison must ignore case, and the POSIX
// `uname -sm; file` hint means nothing there.
func TestHealRemoteRecord_WindowsSamePathOtherCase_StopsWithoutUnameHint(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return strings.ToLower(winQuil) }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		p := winProbe()
		p.ExistingPath, p.ExistingDirWritable = winQuil, true
		return p, nil
	}

	var done, retry bool
	out := captureStderr(t, func() { done, retry = dropProbe(healRemoteRecord("win")) })
	if !done || retry {
		t.Errorf("done, retry = %v, %v; want true, false", done, retry)
	}
	if len(spy.recorded) != 0 {
		t.Errorf("rewrote the record for the same file: %v", spy.recorded)
	}
	if strings.Contains(out, "uname") {
		t.Errorf("printed a POSIX command for a Windows host:\n%s", out)
	}
	if !strings.Contains(out, "will not run there") {
		t.Errorf("no explanation printed:\n%s", out)
	}
}
