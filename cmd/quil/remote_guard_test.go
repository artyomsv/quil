package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestRespawnArgs_ReinjectsRemoteFlag guards a silent host mis-targeting bug.
//
// main() strips --remote from os.Args and keeps the destination only in the
// process-local remoteDest. An update respawn that replays the rewritten argv
// therefore drops --remote entirely, and the replacement process attaches to
// (or starts) the LOCAL daemon while the operator still believes they are on
// the remote host. --dev survives the same rewrite only because it is
// re-exported as QUIL_HOME and the respawn passes os.Environ(); --remote has no
// equivalent carrier.
func TestRespawnArgs_ReinjectsRemoteFlag(t *testing.T) {
	prevArgs := os.Args
	t.Cleanup(func() { os.Args = prevArgs })

	// argv as main() leaves it: --remote already removed.
	os.Args = []string{"quil"}
	withRemote(t, "gpu01")

	got := respawnArgs()

	if len(got) < 2 || got[0] != "--remote" || got[1] != "gpu01" {
		t.Fatalf("respawnArgs() = %v, want it to lead with --remote gpu01", got)
	}
}

// TestRespawnArgs_PreservesOtherArgs pins that re-injection does not displace
// whatever else was on the command line.
func TestRespawnArgs_PreservesOtherArgs(t *testing.T) {
	prevArgs := os.Args
	t.Cleanup(func() { os.Args = prevArgs })

	os.Args = []string{"quil", "restart"}
	withRemote(t, "user@gpu01.example.com")

	got := strings.Join(respawnArgs(), " ")
	want := "--remote user@gpu01.example.com restart"
	if got != want {
		t.Errorf("respawnArgs() = %q, want %q", got, want)
	}
}

// TestRespawnArgs_UntouchedInLocalMode — a local session must respawn exactly
// what it was given; injecting anything here would be the mirror-image bug.
func TestRespawnArgs_UntouchedInLocalMode(t *testing.T) {
	prevArgs := os.Args
	prevDest := remoteDest
	t.Cleanup(func() { os.Args = prevArgs; remoteDest = prevDest })

	remoteDest = ""
	os.Args = []string{"quil", "restart"}

	got := respawnArgs()
	if len(got) != 1 || got[0] != "restart" {
		t.Errorf("respawnArgs() = %v, want [restart] unchanged", got)
	}
	if strings.Contains(strings.Join(got, " "), "--remote") {
		t.Errorf("respawnArgs() injected --remote into a local session: %v", got)
	}
}

// TestValidateRemoteDest_RejectsOptionShapedDestinations covers the argument
// injection path. The destination is passed to ssh after our -o flags, so ssh
// is still in option-parsing mode when it reaches it: a leading '-' makes it an
// OPTION, and -oProxyCommand=... runs an arbitrary command on THIS machine
// before any network traffic. Confirmed against OpenSSH 10.2p1.
func TestValidateRemoteDest_RejectsOptionShapedDestinations(t *testing.T) {
	bad := []string{
		"-oProxyCommand=/tmp/pwn.sh",
		"-oPermitLocalCommand=yes",
		"-obviouslyfake",
		"-v",
		"--",
	}
	for _, dest := range bad {
		t.Run(dest, func(t *testing.T) {
			if err := validateRemoteDest(dest); err == nil {
				t.Errorf("validateRemoteDest(%q) = nil, want rejection", dest)
			}
		})
	}
}

func TestValidateRemoteDest_AcceptsRealDestinations(t *testing.T) {
	good := []string{
		"", // no --remote given at all
		"gpu01",
		"user@gpu01",
		"user@gpu01.example.com",
		"2001:db8::1",
		"my-ssh-config-alias",
	}
	for _, dest := range good {
		t.Run(dest, func(t *testing.T) {
			if err := validateRemoteDest(dest); err != nil {
				t.Errorf("validateRemoteDest(%q) = %v, want nil", dest, err)
			}
		})
	}
}

// TestParseRemoteFlag_RejectsFlagShapedDestination pins that the rejection is
// wired into the parser, not just available as a helper. Without it,
// `quil --remote --json status` arms remote mode with dest "--json" and
// silently swallows the --json flag.
func TestParseRemoteFlag_RejectsFlagShapedDestination(t *testing.T) {
	for _, args := range [][]string{
		{"quil", "--remote", "--json", "status"},
		{"quil", "--remote=-oProxyCommand=/tmp/pwn.sh"},
	} {
		_, _, err := parseRemoteFlag(args)
		if err == nil {
			t.Errorf("parseRemoteFlag(%v) = nil error, want rejection", args)
		}
	}
}

// TestParseRemoteFlag_PositionIndependent pins that the flag is recognised
// wherever it appears. main() parses it before the subcommand switch, so
// `quil status --remote gpu01` must arm remote mode and still dispatch status —
// otherwise a guard the user believed was active silently is not.
func TestParseRemoteFlag_PositionIndependent(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantDest string
		wantRest []string
	}{
		{"leading", []string{"quil", "--remote", "gpu01", "status"}, "gpu01", []string{"quil", "status"}},
		{"after a subcommand", []string{"quil", "status", "--remote", "gpu01"}, "gpu01", []string{"quil", "status"}},
		{"joined form after a subcommand", []string{"quil", "status", "--remote=gpu01"}, "gpu01", []string{"quil", "status"}},
		{"between two args", []string{"quil", "daemon", "--remote", "gpu01", "stop"}, "gpu01", []string{"quil", "daemon", "stop"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest, rest, err := parseRemoteFlag(tt.args)
			if err != nil {
				t.Fatalf("parseRemoteFlag(%v) = %v", tt.args, err)
			}
			if dest != tt.wantDest {
				t.Errorf("dest = %q, want %q", dest, tt.wantDest)
			}
			if strings.Join(rest, " ") != strings.Join(tt.wantRest, " ") {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
			}
		})
	}
}

// TestRemoteLinkEstablished_DefaultsTrueWithoutAProbe pins the fail-safe
// direction. A missing probe means "cannot tell"; reporting unreachable there
// would break every working session rather than mis-explain a broken one.
func TestRemoteLinkEstablished_DefaultsTrueWithoutAProbe(t *testing.T) {
	prev := remoteLinkEstablishedFn
	remoteLinkEstablishedFn = nil
	t.Cleanup(func() { remoteLinkEstablishedFn = prev })

	if !remoteLinkEstablished() {
		t.Error("remoteLinkEstablished() = false with no probe installed, want true")
	}
}

func TestRemoteLinkEstablished_ReportsWhatTheProbeReports(t *testing.T) {
	prev := remoteLinkEstablishedFn
	t.Cleanup(func() { remoteLinkEstablishedFn = prev })

	for _, want := range []bool{true, false} {
		remoteLinkEstablishedFn = func() bool { return want }
		if got := remoteLinkEstablished(); got != want {
			t.Errorf("remoteLinkEstablished() = %v, want %v", got, want)
		}
	}
}

// TestReportRemoteLinkFailure_HandlesNilError covers the still-connecting case:
// the link never delivered a byte but ssh has not exited, so there is no error
// to quote. Printing "<nil>" there would be worse than saying nothing.
func TestReportRemoteLinkFailure_HandlesNilError(t *testing.T) {
	prevDest := remoteDest
	remoteDest = "gpu01"
	t.Cleanup(func() { remoteDest = prevDest })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	prevStderr := os.Stderr
	os.Stderr = w
	reportRemoteLinkFailure(nil)
	os.Stderr = prevStderr
	w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "<nil>") {
		t.Errorf("message leaked a nil error; got:\n%s", out)
	}
	if !strings.Contains(out, "gpu01") {
		t.Errorf("message does not name the host; got:\n%s", out)
	}
}
