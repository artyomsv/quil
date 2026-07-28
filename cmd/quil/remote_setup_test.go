package main

import (
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
