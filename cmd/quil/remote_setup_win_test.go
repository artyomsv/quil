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

// A PowerShell DefaultShell installs like cmd does, with every command quoted
// for PowerShell. Setup used to refuse it before any remote write, because the
// extract step pipes the archive through that shell; measured on Windows 10,
// `& 'C:\Windows\System32\tar.exe' -tvzf -` reads a 2 MB archive from ssh stdin
// under Windows PowerShell and exits 0.
func TestRunRemoteSetup_WindowsPowerShellShell_Installs(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	r := &stepRunner{t: t, steps: []runStep{
		{prefix: "powershell.exe ", out: "__quil_prepare__\r\n" + winStaging + "\r\n" + winTar + "\r\n"},
		{prefix: `& '` + winTar + `' -xf - -C '` + winStaging + `'`},
		{prefix: "powershell.exe ", out: "__quil_install__\r\n" + winQuil + "\r\n"},
		{prefix: `& '` + winQuil + `' daemon install-logon`, out: "registered"},
	}}
	setupRunnerFn = func(string) remoteinstall.Runner { return r }

	probe := winProbe()
	probe.Shell = remoteinstall.ShellPowerShell
	var out bytes.Buffer
	if err := runRemoteSetup("win01", setupOptions{FromDir: winFromDir(t), Yes: true, Out: &out, probe: &probe}); err != nil {
		t.Fatalf("runRemoteSetup: %v\n%s", err, out.String())
	}
	if len(r.ran) != 4 {
		t.Fatalf("ran %d remote commands, want 4 (prepare, tar, finalize, install-logon):\n%q", len(r.ran), r.ran)
	}
	if spy.recorded["win01"] != winQuil || spy.shells["win01"] != remoteinstall.ShellPowerShell {
		t.Errorf("recorded (%q, %q), want (%q, %q)",
			spy.recorded["win01"], spy.shells["win01"], winQuil, remoteinstall.ShellPowerShell)
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
	recordedRemoteShellFn = func(string) string { return remoteinstall.ShellCmd }
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

// A Windows-recorded host whose probe finds no quil is offered the install,
// and its record is NOT cleared: the probe never tests the recorded path, so
// "none found" is not positive evidence against it. A successful install
// records the new path anyway.
func TestOfferRemoteInstall_Probe_WindowsRecordedNoQuil_OffersInstallKeepsRecord(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	isReleaseFn = func() bool { return false } // runRemoteSetup stops at plannedVersion
	recordedRemoteBinaryFn = func(string) string { return `C:\old\quil.exe` }
	recordedRemoteShellFn = func(string) string { return remoteinstall.ShellCmd }
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
	if len(spy.cleared) != 0 || len(spy.recorded) != 0 {
		t.Errorf("record mutated on the exit-1 path: cleared %v recorded %v", spy.cleared, spy.recorded)
	}
	if remoteFailureReported {
		t.Error("flagged as reported, which would hide the install's own failure")
	}
}

// First contact (no record): the host may be Windows, so exit 1 is probed,
// and a probe that finds nothing offers the install — with nothing to clear.
func TestOfferRemoteInstall_Probe_NoRecordNoQuil_OffersInstall(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	isReleaseFn = func() bool { return false }
	calls := 0
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		calls++
		return winProbe(), nil
	}

	out := captureStderr(t, func() { offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if calls != 1 {
		t.Errorf("probed %d times, want 1", calls)
	}
	if !strings.Contains(out, "Quil is not installed on win") || !strings.Contains(out, "development build") {
		t.Errorf("install not offered:\n%s", out)
	}
	if len(spy.cleared) != 0 || len(spy.recorded) != 0 {
		t.Errorf("record mutated: cleared %v recorded %v", spy.cleared, spy.recorded)
	}
}

// A host recorded as POSIX says 127 for a missing command, so its exit 1 is
// quil's own: no probe, no "Checking…", no record change — RemedyNone, as it
// was before Windows support.
func TestOfferRemoteInstall_Probe_POSIXRecorded_NoProbe(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return "/home/a/.local/bin/quil" }
	probed := false
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		probed = true
		return remoteinstall.Probe{}, nil
	}

	var retry bool
	out := captureStderr(t, func() { retry = offerRemoteInstall("gpu01", remoteinstall.RemedyProbe) })
	if retry || probed || remoteFailureReported {
		t.Errorf("retry %v probed %v reported %v; want none", retry, probed, remoteFailureReported)
	}
	if out != "" {
		t.Errorf("printed for a POSIX exit 1, which the gate reports itself:\n%s", out)
	}
	if len(spy.cleared) != 0 || len(spy.recorded) != 0 {
		t.Errorf("record mutated: cleared %v recorded %v", spy.cleared, spy.recorded)
	}
}

// The same gate end to end: the version gate prints its usual link failure
// for a POSIX-recorded host, and the record is untouched.
func TestGateVersionCheck_POSIXRecordedExitOne_ReportsLinkFailure(t *testing.T) {
	withRemote(t, "gpu01")
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	remoteGateSeams(t, false, 1)
	offerRemoteInstallFn = offerRemoteInstall // the real one, gate included
	recordedRemoteBinaryFn = func(string) string { return "/home/a/.local/bin/quil" }
	probed := false
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		probed = true
		return remoteinstall.Probe{}, nil
	}
	exitCode := -1
	exitFn = func(code int) { exitCode = code }

	out := captureStderr(t, func() { gateVersionCheck(deadClient(t)) })
	if probed {
		t.Error("probed a POSIX-recorded host for exit 1")
	}
	if exitCode != 1 || !strings.Contains(out, "Cannot reach the Quil daemon on gpu01") {
		t.Errorf("exit %d, want 1 with the link-failure report:\n%s", exitCode, out)
	}
	if len(spy.cleared) != 0 || len(spy.recorded) != 0 {
		t.Errorf("record mutated: cleared %v recorded %v", spy.cleared, spy.recorded)
	}
}

// A POSIX host reached for the first time (no record) is probed, but a POSIX
// shell would have answered 127 for a missing path — so a quil found in an
// adoptable directory is quil's own exit, not a path to adopt.
func TestOfferRemoteInstall_Probe_POSIXProbeFindsQuil_ReportsExitNoAdopt(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{Home: "/home/a", ExistingPath: "/home/a/.local/bin/quil", ExistingDirWritable: true}, nil
	}

	var retry bool
	out := captureStderr(t, func() { retry = offerRemoteInstall("gpu01", remoteinstall.RemedyProbe) })
	if retry || !remoteFailureReported || !strings.Contains(out, "exited before its daemon answered") {
		t.Errorf("retry %v reported %v:\n%s", retry, remoteFailureReported, out)
	}
	if len(spy.recorded) != 0 {
		t.Errorf("adopted a path on a POSIX host: %v", spy.recorded)
	}
}

// POSITIVE EVIDENCE ONLY: a probe that failed says nothing, so nothing changes
// and the caller reports the link failure exactly as before.
func TestOfferRemoteInstall_Probe_Error_NoRecordChange(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	isReleaseFn = func() bool { return false }
	recordedRemoteBinaryFn = func(string) string { return winQuil }
	recordedRemoteShellFn = func(string) string { return remoteinstall.ShellCmd }
	probed := false
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		probed = true
		return remoteinstall.Probe{}, errors.New("host down")
	}

	var retry bool
	out := captureStderr(t, func() { retry = offerRemoteInstall("win", remoteinstall.RemedyProbe) })
	if !probed {
		t.Fatal("the probe never ran, so this test proves nothing")
	}
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
	recordedRemoteShellFn = func(string) string { return remoteinstall.ShellCmd } // unchanged shell
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

// The documented workflow: install with cmd as DefaultShell, then switch back
// to PowerShell. The record still says cmd, so every attach sends a command
// quoted for cmd to PowerShell. The probe finds the SAME file under the new
// shell; both reconciliation paths must re-record the shell and re-dial, and
// the re-dial must then settle without a second rewrite. The config is
// simulated by the spy: the second pass reads back what the first recorded.
func TestOfferRemoteInstall_WindowsShellChangedSamePath_RecordsShellOnceAndRetries(t *testing.T) {
	for _, remedy := range []remoteinstall.Remedy{remoteinstall.RemedyInstall, remoteinstall.RemedyProbe} {
		t.Run(remedyName(remedy), func(t *testing.T) {
			resetRemoteSetupState(t)
			spy := newHealSpy(t)
			isReleaseFn = func() bool { return false } // an install attempt would print "development build"
			recordedRemoteBinaryFn = func(dest string) string {
				if p, ok := spy.recorded[dest]; ok {
					return p
				}
				return winQuil
			}
			recordedRemoteShellFn = func(dest string) string {
				if s, ok := spy.shells[dest]; ok {
					return s
				}
				return remoteinstall.ShellCmd
			}
			probeRemoteFn = func(string) (remoteinstall.Probe, error) {
				p := winProbe()
				p.Shell = remoteinstall.ShellPowerShell
				p.ExistingPath, p.ExistingDirWritable = winQuil, true
				return p, nil
			}

			var retry bool
			out := captureStderr(t, func() { retry = offerRemoteInstall("win", remedy) })
			if !retry {
				t.Fatalf("no re-dial after the shell changed:\n%s", out)
			}
			if spy.recorded["win"] != winQuil || spy.shells["win"] != remoteinstall.ShellPowerShell {
				t.Errorf("recorded %q shell %q, want %q shell %q",
					spy.recorded["win"], spy.shells["win"], winQuil, remoteinstall.ShellPowerShell)
			}
			want := "Found quil at " + winQuil + " on win; its ssh shell is now powershell. Reconnecting"
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
			if strings.Contains(out, "development build") || strings.Contains(out, "will not run there") {
				t.Errorf("treated a shell change as an install or a broken binary:\n%s", out)
			}

			// The re-dial fails again for some other reason: the pair is now
			// unchanged, so the existing handling applies and nothing is
			// rewritten a second time.
			spy.recorded, spy.shells = map[string]string{"win": winQuil}, map[string]string{"win": remoteinstall.ShellPowerShell}
			rewrites := 0
			recordRemoteBinaryFn = func(string, string, string) error { rewrites++; return nil }
			out = captureStderr(t, func() { retry = offerRemoteInstall("win", remedy) })
			if retry || rewrites != 0 {
				t.Errorf("second pass: retry %v, rewrites %d; want the unchanged-pair handling:\n%s", retry, rewrites, out)
			}
		})
	}
}

// The unchanged-pair guard is not weakened: a Windows probe at the recorded
// path under the recorded shell never rewrites, on either path.
func TestOfferRemoteInstall_WindowsSamePathSameShell_NoRewrite(t *testing.T) {
	for _, remedy := range []remoteinstall.Remedy{remoteinstall.RemedyInstall, remoteinstall.RemedyProbe} {
		t.Run(remedyName(remedy), func(t *testing.T) {
			resetRemoteSetupState(t)
			spy := newHealSpy(t)
			recordedRemoteBinaryFn = func(string) string { return winQuil }
			recordedRemoteShellFn = func(string) string { return remoteinstall.ShellPowerShell }
			probeRemoteFn = func(string) (remoteinstall.Probe, error) {
				p := winProbe()
				p.Shell = remoteinstall.ShellPowerShell
				p.ExistingPath, p.ExistingDirWritable = winQuil, true
				return p, nil
			}
			var retry bool
			captureStderr(t, func() { retry = offerRemoteInstall("win", remedy) })
			if retry || len(spy.recorded) != 0 {
				t.Errorf("retry %v recorded %v; want neither", retry, spy.recorded)
			}
		})
	}
}

// Positive evidence only: a probe that failed never changes the shell.
func TestOfferRemoteInstall_WindowsShellProbeError_NoRecordChange(t *testing.T) {
	for _, remedy := range []remoteinstall.Remedy{remoteinstall.RemedyInstall, remoteinstall.RemedyProbe} {
		t.Run(remedyName(remedy), func(t *testing.T) {
			resetRemoteSetupState(t)
			spy := newHealSpy(t)
			isReleaseFn = func() bool { return false }
			recordedRemoteBinaryFn = func(string) string { return winQuil }
			recordedRemoteShellFn = func(string) string { return remoteinstall.ShellCmd }
			probed := false
			probeRemoteFn = func(string) (remoteinstall.Probe, error) {
				probed = true
				return remoteinstall.Probe{}, errors.New("host down")
			}
			var retry bool
			captureStderr(t, func() { retry = offerRemoteInstall("win", remedy) })
			if !probed {
				t.Fatal("the probe never ran, so this test proves nothing")
			}
			if retry || len(spy.recorded) != 0 || len(spy.cleared) != 0 {
				t.Errorf("retry %v recorded %v cleared %v; want none", retry, spy.recorded, spy.cleared)
			}
		})
	}
}

// Only a WINDOWS probe carries a shell worth re-recording. A POSIX probe at
// the recorded path keeps the wrong-architecture answer, even under a record
// whose shell differs from the probe's.
func TestHealRemoteRecord_POSIXProbeSamePath_ShellNotRewritten(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	const path = "/home/a/.local/bin/quil"
	recordedRemoteBinaryFn = func(string) string { return path }
	recordedRemoteShellFn = func(string) string { return remoteinstall.ShellCmd }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{Home: "/home/a", ExistingPath: path, ExistingDirWritable: true}, nil
	}

	var done, retry bool
	out := captureStderr(t, func() { done, retry = dropProbe(healRemoteRecord("gpu01")) })
	if !done || retry || len(spy.recorded) != 0 {
		t.Errorf("done %v retry %v recorded %v; want the unchanged wrong-arch stop", done, retry, spy.recorded)
	}
	if !strings.Contains(out, "uname") {
		t.Errorf("POSIX wrong-arch hint missing:\n%s", out)
	}
}

func remedyName(r remoteinstall.Remedy) string {
	switch r {
	case remoteinstall.RemedyInstall:
		return "exit 127 heal"
	case remoteinstall.RemedyProbe:
		return "exit 1 probe"
	}
	return "other"
}
