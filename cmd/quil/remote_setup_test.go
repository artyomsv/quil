package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/remoteinstall"
)

func resetRemoteSetupState(t *testing.T) {
	t.Helper()
	prevRecorded := recordedRemoteBinaryFn
	prevProbe := probeRemoteFn
	prevRecord := recordRemoteBinaryFn
	prevClear := clearRemoteBinaryFn
	prevIsRelease := isReleaseFn
	prevDest := remoteDest
	t.Cleanup(func() {
		recordedRemoteBinaryFn = prevRecorded
		probeRemoteFn = prevProbe
		recordRemoteBinaryFn = prevRecord
		clearRemoteBinaryFn = prevClear
		isReleaseFn = prevIsRelease
		remoteDest = prevDest
	})
	// Default to a host that answers "nothing installed" so a test which does
	// not care about the probe cannot accidentally reach the real ssh path.
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{}, nil
	}
	recordedRemoteBinaryFn = func(string) string { return "" }
	// The same argument applies with MORE force to the two writers: the accident
	// there is not a slow test, it is a write to the developer's real
	// ~/.quil/config.toml, which .claude/rules/dev-environment.md forbids
	// touching. Fail loudly instead of defaulting to the live implementation —
	// a test that needs these calls newHealSpy.
	recordRemoteBinaryFn = func(dest, binary string) error {
		t.Errorf("unstubbed recordRemoteBinaryFn(%q, %q) — call newHealSpy first", dest, binary)
		return nil
	}
	clearRemoteBinaryFn = func(dest string) error {
		t.Errorf("unstubbed clearRemoteBinaryFn(%q) — call newHealSpy first", dest)
		return nil
	}
}

// healSpy captures which config mutation a branch chose. Asserting on the
// mutation rather than only the return value is the point: healing a stale
// record and destroying a good one are both "no install offered" from outside.
type healSpy struct {
	cleared  []string
	recorded map[string]string
}

// dropProbe discards healRemoteRecord's probe return so a test asserting only
// the control flow reads as two values rather than three.
func dropProbe(_ *remoteinstall.Probe, done, retry bool) (bool, bool) {
	return done, retry
}

func newHealSpy(t *testing.T) *healSpy {
	t.Helper()
	s := &healSpy{recorded: map[string]string{}}
	clearRemoteBinaryFn = func(dest string) error {
		s.cleared = append(s.cleared, dest)
		return nil
	}
	recordRemoteBinaryFn = func(dest, binary string) error {
		s.recorded[dest] = binary
		return nil
	}
	return s
}

// ssh parses a leading '-' as an option, and -oProxyCommand= executes a command
// on THIS machine before any network traffic. This is a separate entry point
// from parseRemoteFlag, so it needs its own guard.
func TestRunRemoteSetup_RejectsOptionLikeDestination(t *testing.T) {
	resetRemoteSetupState(t)
	err := runRemoteSetup("-oProxyCommand=touch /tmp/pwned", setupOptions{Yes: true})
	if err == nil {
		t.Fatal("error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "must not begin with '-'") {
		t.Errorf("error %q does not explain the rejection", err)
	}
}

func TestRunRemoteSetup_RejectsEmptyDestination(t *testing.T) {
	resetRemoteSetupState(t)
	if err := runRemoteSetup("", setupOptions{Yes: true}); err == nil {
		t.Fatal("error = nil, want rejection of an empty destination")
	}
}

// A dev build has no matching release. Installing "latest" instead would turn
// a missing binary into a version mismatch, so it must refuse and name both
// ways out.
func TestPlannedVersion_RefusesDevBuildWithoutASource(t *testing.T) {
	resetRemoteSetupState(t)
	isReleaseFn = func() bool { return false }

	_, err := plannedVersion(setupOptions{}, remoteinstall.Platform{GOOS: "linux", GOARCH: "amd64"})
	if err == nil {
		t.Fatal("error = nil, want refusal")
	}
	for _, want := range []string{"--from-dir", "--version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

// The loop guard, re-keyed onto a fact. A binary that will not execute reports
// 127 — the same status as "not installed" — so without this a launch would
// install, retry, and offer forever. What makes it terminate is that the probe
// reports the SAME path we ran: "offer forever" needs a path that exists, which
// becomes true only after a successful write.
func TestHealRemoteRecord_ProbeMatchesRecord_RefusesASecondAttempt(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	const path = "/home/a/.local/bin/quil"
	recordedRemoteBinaryFn = func(string) string { return path }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{ExistingPath: path}, nil
	}

	if offerRemoteInstall("gpu01", remoteinstall.RemedyInstall) {
		t.Error("offered an install for a binary the host has and cannot run")
	}
	if offerRemoteInstall("gpu01", remoteinstall.RemedyReinstall) {
		t.Error("offered a second install for a reinstall remedy")
	}
	if len(spy.cleared) != 0 {
		t.Errorf("cleared the record for a binary that is actually there: %v", spy.cleared)
	}
}

// The shipped bug. The host's binary was deleted, so the recorded path is
// known-false — but the old guard read the record as proof of an install and
// printed an architecture theory instead of offering to reinstall.
func TestHealRemoteRecord_ProbeFindsNothing_ClearsRecordAndOffers(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return "/home/a/.local/bin/quil" }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{ExistingPath: ""}, nil
	}

	_, done, retry := healRemoteRecord("gpu01")
	if done {
		t.Error("done = true, want the caller to carry on and offer an install")
	}
	if retry {
		t.Error("retry = true, want false — nothing has been fixed yet")
	}
	if len(spy.cleared) != 1 || spy.cleared[0] != "gpu01" {
		t.Errorf("cleared = %v, want exactly [gpu01]", spy.cleared)
	}
}

// An admin moved the binary. quil is there, just not where we looked — so the
// record is wrong rather than stale, and reinstalling would be the wrong fix.
func TestHealRemoteRecord_ProbeFindsDifferentPath_RecordsAndRetries(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return "/home/a/.local/bin/quil" }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{ExistingPath: "/usr/local/bin/quil", ExistingDirWritable: true}, nil
	}

	_, done, retry := healRemoteRecord("gpu01")
	if !done || !retry {
		t.Errorf("done, retry = %v, %v; want true, true so the caller re-dials", done, retry)
	}
	if got := spy.recorded["gpu01"]; got != "/usr/local/bin/quil" {
		t.Errorf("recorded = %q, want the path the probe found", got)
	}
	if len(spy.cleared) != 0 {
		t.Errorf("cleared a record that should have been corrected: %v", spy.cleared)
	}
}

// The same branch covers a host installed by hand and never recorded: we dialed
// a bare `quil`, the non-interactive PATH could not see it, and the probe found
// it by absolute path. offerRemoteInstall's own comment already flagged this as
// contradicting the probe's summary one line later.
func TestHealRemoteRecord_HandInstalledUnrecorded_RecordsAndRetries(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return "" }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{ExistingPath: "/opt/quil/bin/quil", ExistingDirWritable: true}, nil
	}

	_, done, retry := healRemoteRecord("gpu01")
	if !done || !retry {
		t.Errorf("done, retry = %v, %v; want true, true", done, retry)
	}
	if got := spy.recorded["gpu01"]; got != "/opt/quil/bin/quil" {
		t.Errorf("recorded = %q, want the hand-installed path", got)
	}
}

// A directory writable by group or other must NOT be adopted. Recording a path
// is what makes it the ssh remote command on every later attach with no further
// prompt, so adopting a shared /opt/bin or a group-writable /usr/local/bin (the
// Homebrew default on multi-admin macOS) would let another local user on that
// host replace the binary and run as us. PlanTarget already refuses such a
// directory for an in-place upgrade; the reconciliation has to refuse it too,
// or it becomes a way around that guard which never prompts.
func TestHealRemoteRecord_GroupWritableDir_NotAdopted(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return "" }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{
			ExistingPath:        "/usr/local/bin/quil",
			ExistingDirWritable: false, // the probe reported "ro"
		}, nil
	}

	done, retry := dropProbe(healRemoteRecord("gpu01"))
	if done || retry {
		t.Errorf("done, retry = %v, %v; want false, false so the install is offered instead", done, retry)
	}
	if len(spy.recorded) != 0 {
		t.Errorf("adopted a directory writable by group or other: %v", spy.recorded)
	}
}

// The suggested command is pasted into the user's OWN shell, and `path` is
// remote-reported — checkRemotePath admits an apostrophe. Interpolating it raw
// inside single quotes lets a crafted path close the quote and append a command
// that runs LOCALLY. It also breaks on legitimate input: this package's own
// config test uses /home/o'brien/bin/quil.
func TestReportRemoteBinaryWontRun_QuotesTheSuggestedCommand(t *testing.T) {
	const evil = `/tmp/a'; touch /tmp/pwned; echo '`
	out := captureStderr(t, func() { reportRemoteBinaryWontRun("gpu01", evil) })

	// The payload must not sit outside a quoted region. Everything after the
	// `ssh gpu01 ` prefix is one ShellSingleQuote'd argument, so the injected
	// apostrophe has to appear in its escaped form.
	if strings.Contains(out, "; touch /tmp/pwned;") && !strings.Contains(out, `'\''`) {
		t.Errorf("suggested command lets a crafted path break out of the quotes:\n%s", out)
	}
	if !strings.Contains(out, `'\''`) {
		t.Errorf("apostrophe was not shell-escaped, so the line cannot be pasted:\n%s", out)
	}
}

// The probe must run exactly ONCE across the attach path. Both probe sites
// announce themselves with "Checking <dest>…", so counting that line is what
// distinguishes one ssh round trip from two — and a regression that made
// runRemoteSetup ignore opts.probe and re-probe would otherwise pass every
// other test in this file while silently doubling the wait on a failed launch.
func TestRunRemoteSetup_ReusesASuppliedProbe(t *testing.T) {
	resetRemoteSetupState(t)
	newHealSpy(t)
	// A dev build refuses at plannedVersion, which runs immediately after the
	// probe-reuse block — far enough to prove the branch, short of the network.
	isReleaseFn = func() bool { return false }

	probe := remoteinstall.Probe{
		Home:         "/home/a",
		Platform:     remoteinstall.Platform{GOOS: "linux", GOARCH: "amd64"},
		ExistingPath: "",
	}
	out := captureStderr(t, func() {
		_ = runRemoteSetup("gpu01", setupOptions{probe: &probe})
	})

	if n := strings.Count(out, "Checking"); n != 0 {
		t.Errorf("probed again despite being handed a probe (%d times):\n%s", n, out)
	}
}

func TestOfferRemoteInstall_ProbesExactlyOnce(t *testing.T) {
	resetRemoteSetupState(t)
	newHealSpy(t)
	isReleaseFn = func() bool { return false }
	calls := 0
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		calls++
		return remoteinstall.Probe{
			Home:     "/home/a",
			Platform: remoteinstall.Platform{GOOS: "linux", GOARCH: "amd64"},
		}, nil
	}

	out := captureStderr(t, func() {
		offerRemoteInstall("gpu01", remoteinstall.RemedyInstall)
	})

	if calls != 1 {
		t.Errorf("probeRemoteFn called %d times, want exactly 1", calls)
	}
	if n := strings.Count(out, "Checking"); n != 1 {
		t.Errorf("announced a probe %d times, want exactly 1:\n%s", n, out)
	}
}

// The record and clear paths are stubbed everywhere else in this file, so the
// real load → mutate → save — including the fs.ErrNotExist tolerance the
// comment on mutateConfig calls load-bearing for a fresh machine's first
// remote — is otherwise never executed. QUIL_HOME redirects config.ConfigPath
// into a temp dir, so nothing here can touch the developer's real ~/.quil.
func TestRecordAndClearRemoteBinary_RoundTripOnDisk(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	// First write against a config.toml that does not exist yet.
	if err := recordRemoteBinary("gpu01", "/home/a/.local/bin/quil"); err != nil {
		t.Fatalf("recordRemoteBinary on a missing config: %v", err)
	}
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		t.Fatalf("load after record: %v", err)
	}
	if got := cfg.RemoteBinary("gpu01"); got != "/home/a/.local/bin/quil" {
		t.Fatalf("RemoteBinary = %q, want the recorded path", got)
	}
	// A default config must have been written, not a zeroed one — otherwise the
	// first remote provisioned on a fresh machine silently resets every setting.
	if cfg.Daemon.SnapshotInterval == "" {
		t.Error("recording into a missing config wrote a zeroed config, not a default one")
	}

	if err := clearRemoteBinary("gpu01"); err != nil {
		t.Fatalf("clearRemoteBinary: %v", err)
	}
	cfg, err = config.Load(config.ConfigPath())
	if err != nil {
		t.Fatalf("load after clear: %v", err)
	}
	if got := cfg.RemoteBinary("gpu01"); got != "" {
		t.Errorf("RemoteBinary = %q after clear, want empty", got)
	}
}

// POSITIVE EVIDENCE ONLY, and the case most likely to be lost in a later
// refactor. A probe that failed says nothing about whether the binary is there:
// the host may be down or the key rejected. Clearing on it would downgrade a
// working host to a bare `quil` on the non-interactive PATH — invisible on
// Debian and Ubuntu, and the exact failure the record exists to prevent.
func TestHealRemoteRecord_ProbeError_LeavesRecordUntouched(t *testing.T) {
	resetRemoteSetupState(t)
	spy := newHealSpy(t)
	recordedRemoteBinaryFn = func(string) string { return "/home/a/.local/bin/quil" }
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		return remoteinstall.Probe{}, errors.New("ssh: connect to host gpu01 port 22: No route to host")
	}

	_, done, retry := healRemoteRecord("gpu01")
	if done || retry {
		t.Errorf("done, retry = %v, %v; want false, false so setup reports the real error", done, retry)
	}
	if len(spy.cleared) != 0 {
		t.Errorf("cleared the record on a FAILED probe: %v", spy.cleared)
	}
	if len(spy.recorded) != 0 {
		t.Errorf("rewrote the record on a FAILED probe: %v", spy.recorded)
	}
}

// An upgrade means quil RAN over there, so the binary is fine and no probe
// result could change the decision. Probing anyway would spend an ssh round
// trip to answer a question already settled.
func TestOfferRemoteInstall_UpgradeRemedy_SkipsTheProbe(t *testing.T) {
	resetRemoteSetupState(t)
	newHealSpy(t)
	probed := false
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		probed = true
		return remoteinstall.Probe{}, nil
	}
	// Stop before the install itself: a dev build refuses to resolve a version,
	// which is enough to prove the guard was passed without running ssh.
	isReleaseFn = func() bool { return false }

	offerRemoteInstall("gpu01", remoteinstall.RemedyUpgrade)

	if probed {
		t.Error("probed the host for an upgrade, where the binary is known to run")
	}
}

func TestOfferRemoteInstall_IgnoresRemedyNone(t *testing.T) {
	resetRemoteSetupState(t)
	newHealSpy(t)
	probed := false
	probeRemoteFn = func(string) (remoteinstall.Probe, error) {
		probed = true
		return remoteinstall.Probe{}, nil
	}

	if offerRemoteInstall("gpu01", remoteinstall.RemedyNone) {
		t.Error("offered an install for a failure that is not about a missing binary")
	}
	if probed {
		t.Error("probed the host for a failure that is not about a missing binary")
	}
}

func TestSSHRunner_UsesTheGivenDestination(t *testing.T) {
	// A destination ssh would read as an option must be rejected by the shared
	// transport guard rather than reaching exec.
	r := sshRunner{dest: "-oProxyCommand=id"}
	code, err := r.Run(t.Context(), "true", nil, nil, nil)
	if err == nil {
		t.Fatal("error = nil, want the transport guard to reject it")
	}
	if code != -1 {
		t.Errorf("code = %d, want -1", code)
	}
}

func TestDisplayVersion(t *testing.T) {
	if got := displayVersion(remoteinstall.Source{Version: "1.43.1"}); got != "quil 1.43.1" {
		t.Errorf("displayVersion = %q", got)
	}
	if got := displayVersion(remoteinstall.Source{}); !strings.Contains(got, "locally built") {
		t.Errorf("displayVersion = %q, want it to name a local build", got)
	}
}

func TestParseRemoteArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantDest  string
		wantOpts  setupOptions
		wantUsage bool
		wantErr   string
	}{
		{name: "destination only", args: []string{"gpu01"}, wantDest: "gpu01"},
		{name: "yes long", args: []string{"gpu01", "--yes"}, wantDest: "gpu01", wantOpts: setupOptions{Yes: true}},
		{name: "yes short", args: []string{"-y", "gpu01"}, wantDest: "gpu01", wantOpts: setupOptions{Yes: true}},
		{
			name: "from-dir separate value", args: []string{"gpu01", "--from-dir", "./dist"},
			wantDest: "gpu01", wantOpts: setupOptions{FromDir: "./dist"},
		},
		{
			name: "from-dir equals form", args: []string{"gpu01", "--from-dir=./dist"},
			wantDest: "gpu01", wantOpts: setupOptions{FromDir: "./dist"},
		},
		{
			name: "version separate value", args: []string{"gpu01", "--version", "1.43.1"},
			wantDest: "gpu01", wantOpts: setupOptions{Version: "1.43.1"},
		},
		{
			name: "version equals form", args: []string{"--version=1.43.1", "gpu01"},
			wantDest: "gpu01", wantOpts: setupOptions{Version: "1.43.1"},
		},
		{name: "help long", args: []string{"--help"}, wantUsage: true},
		{name: "help short", args: []string{"-h", "gpu01"}, wantUsage: true},

		{name: "missing from-dir value", args: []string{"gpu01", "--from-dir"}, wantErr: "requires a value"},
		{name: "empty from-dir value", args: []string{"gpu01", "--from-dir="}, wantErr: "requires a value"},
		{name: "missing version value", args: []string{"gpu01", "--version"}, wantErr: "requires a value"},
		{name: "empty version value", args: []string{"gpu01", "--version="}, wantErr: "requires a value"},
		{name: "unknown flag", args: []string{"gpu01", "--force"}, wantErr: "unknown flag"},
		{name: "two destinations", args: []string{"gpu01", "gpu02"}, wantErr: "unexpected argument"},
		// --from-dir wins in resolveSource, so accepting both would silently
		// ignore one of them.
		{
			name:    "mutually exclusive sources",
			args:    []string{"gpu01", "--from-dir", "./dist", "--version", "1.43.1"},
			wantErr: "mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, dest, usage, err := parseRemoteArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("error = nil, want one containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if usage != tt.wantUsage {
				t.Errorf("showUsage = %v, want %v", usage, tt.wantUsage)
			}
			if usage {
				return
			}
			if dest != tt.wantDest {
				t.Errorf("dest = %q, want %q", dest, tt.wantDest)
			}
			if opts != tt.wantOpts {
				t.Errorf("opts = %+v, want %+v", opts, tt.wantOpts)
			}
		})
	}
}

// remoteSSHOptions decides between `ssh host quil --stdio` and the recorded
// absolute path — the thing that makes attaching work when the remote's
// non-interactive PATH cannot see the install directory.
func TestRemoteSSHOptions(t *testing.T) {
	prevDest := remoteDest
	t.Cleanup(func() { remoteDest = prevDest })
	remoteDest = "gpu01"

	t.Run("no recorded binary falls back to the transport default", func(t *testing.T) {
		if got := remoteSSHOptions(config.Config{}).RemoteCommand; got != "" {
			t.Errorf("RemoteCommand = %q, want empty so transport's default applies", got)
		}
	})

	t.Run("recorded binary becomes the remote command", func(t *testing.T) {
		var cfg config.Config
		cfg.SetRemoteBinary("gpu01", "/home/a/.local/bin/quil")
		got := remoteSSHOptions(cfg).RemoteCommand
		if want := `'/home/a/.local/bin/quil' --stdio`; got != want {
			t.Errorf("RemoteCommand = %q, want %q", got, want)
		}
	})

	t.Run("a path with an apostrophe is escaped", func(t *testing.T) {
		var cfg config.Config
		cfg.SetRemoteBinary("gpu01", "/home/o'brien/bin/quil")
		got := remoteSSHOptions(cfg).RemoteCommand
		if !strings.Contains(got, `'\''brien`) {
			t.Errorf("RemoteCommand = %q, want the apostrophe escaped", got)
		}
	})

	t.Run("another host's entry is not used", func(t *testing.T) {
		var cfg config.Config
		cfg.SetRemoteBinary("other-host", "/opt/quil")
		if got := remoteSSHOptions(cfg).RemoteCommand; got != "" {
			t.Errorf("RemoteCommand = %q, want empty for an unrecorded destination", got)
		}
	})
}

// The pre-install line may claim the host lacks quil ONLY when the probe proved
// it. Exit 127 alone does not: it means the remote SHELL could not find quil,
// and a binary in ~/.local/bin is invisible to a non-interactive shell, which is
// the problem this feature exists to solve. Saying "not installed" on the exit
// code alone contradicted the probe's own "currently installed: …" one line
// later on every hand-installed host.
//
// The probe now runs first, so this is no longer a prohibition but a
// precondition — which is the stronger property, and the one worth pinning.
func TestOfferRemoteInstall_ClaimsTheHostLacksQuilOnlyWhenProbed(t *testing.T) {
	// isReleaseFn stops runRemoteSetup before it reaches the network in both
	// subtests; all we assert on is what was printed before that.
	t.Run("probe failed — must not claim it", func(t *testing.T) {
		resetRemoteSetupState(t)
		newHealSpy(t)
		isReleaseFn = func() bool { return false }
		probeRemoteFn = func(string) (remoteinstall.Probe, error) {
			return remoteinstall.Probe{}, errors.New("no route to host")
		}

		out := captureStderr(t, func() {
			offerRemoteInstall("nonexistent.invalid", remoteinstall.RemedyInstall)
		})

		if strings.Contains(out, "not installed") {
			t.Errorf("claims the host lacks quil on a FAILED probe, which proves nothing:\n%s", out)
		}
		if !strings.Contains(out, "could not be started") {
			t.Errorf("does not report what actually happened:\n%s", out)
		}
	})

	// Exit 126 says the shell found something and could not exec it, but the
	// probe's discovery test is `[ -x "$p" ]` — so a binary whose execute bit
	// was removed yields 126 from the shell and "" from the probe. Announcing
	// "present but will not execute" there would contradict the probe that just
	// reported the host has none.
	t.Run("probe confirms none — may say so even for a reinstall remedy", func(t *testing.T) {
		resetRemoteSetupState(t)
		newHealSpy(t)
		isReleaseFn = func() bool { return false }
		probeRemoteFn = func(string) (remoteinstall.Probe, error) {
			return remoteinstall.Probe{ExistingPath: ""}, nil
		}

		out := captureStderr(t, func() {
			offerRemoteInstall("nonexistent.invalid", remoteinstall.RemedyReinstall)
		})

		if strings.Contains(out, "will not execute") {
			t.Errorf("claims a binary is present when the probe reported none:\n%s", out)
		}
		if !strings.Contains(out, "not installed") {
			t.Errorf("does not report the probe's finding:\n%s", out)
		}
	})
}
