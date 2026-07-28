package main

import (
	"github.com/artyomsv/quil/internal/config"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/remoteinstall"
)

func resetRemoteSetupState(t *testing.T) {
	t.Helper()
	prevProvisioned := alreadyProvisionedFn
	prevIsRelease := isReleaseFn
	prevDest := remoteDest
	t.Cleanup(func() {
		alreadyProvisionedFn = prevProvisioned
		isReleaseFn = prevIsRelease
		remoteDest = prevDest
	})
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

// The loop guard. A binary that will not execute reports 127 — the same status
// as "not installed" — so without this a launch would install, retry, and offer
// forever.
func TestOfferRemoteInstall_RefusesASecondAttempt(t *testing.T) {
	resetRemoteSetupState(t)
	// A recorded binary path is what says "we already installed here".
	alreadyProvisionedFn = func(string) bool { return true }

	if offerRemoteInstall("gpu01", remoteinstall.RemedyInstall) {
		t.Error("offered a second install in the same process")
	}
	if offerRemoteInstall("gpu01", remoteinstall.RemedyReinstall) {
		t.Error("offered a second install for a reinstall remedy")
	}
}

func TestOfferRemoteInstall_IgnoresRemedyNone(t *testing.T) {
	resetRemoteSetupState(t)
	provisioned := false
	alreadyProvisionedFn = func(string) bool { return provisioned }

	if offerRemoteInstall("gpu01", remoteinstall.RemedyNone) {
		t.Error("offered an install for a failure that is not about a missing binary")
	}
	if provisioned {
		t.Error("RemedyNone should not have provisioned anything")
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

// Everything offerRemoteInstall prints happens BEFORE the probe, so it may only
// claim what the exit code proves. Exit 127 means the remote SHELL could not
// find quil — a binary in ~/.local/bin is invisible to a non-interactive shell,
// which is the problem this feature exists to solve. Claiming "not installed"
// contradicted the probe's own "currently installed: …" one line later.
func TestOfferRemoteInstall_DoesNotClaimTheHostLacksQuil(t *testing.T) {
	resetRemoteSetupState(t)
	alreadyProvisionedFn = func(string) bool { return false }

	// runRemoteSetup will fail at the probe (no such host); we only care about
	// what was printed before it.
	out := captureStderr(t, func() { offerRemoteInstall("nonexistent.invalid", remoteinstall.RemedyInstall) })

	if strings.Contains(out, "not installed") {
		t.Errorf("claims the host lacks quil, which exit 127 does not prove:\n%s", out)
	}
	if !strings.Contains(out, "could not be started") {
		t.Errorf("does not report what actually happened:\n%s", out)
	}
}
