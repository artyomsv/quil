package remoteinstall

import (
	"fmt"
	"strings"
)

// Platform is a remote host's Go build target.
type Platform struct {
	GOOS   string
	GOARCH string
}

func (p Platform) String() string { return p.GOOS + "/" + p.GOARCH }

// PlatformFor maps `uname -s` and `uname -m` onto a Go build target, accepting
// ONLY the combinations quil publishes a release archive for.
//
// Anything else is an error rather than a nearest guess, because a wrong guess
// does not fail cleanly: a binary for the wrong architecture makes the remote
// shell report 127 or 126, and 127 is the same code as "not installed" — so a
// guess turns a clear refusal into an install that appears to succeed and then
// silently repeats.
//
// 32-bit ARM is the live case rather than a hypothetical one. A Raspberry Pi
// running a 64-bit kernel with a 32-bit userland reports aarch64 from uname -m
// while its dynamic loader is armhf, so even a "correct" reading of uname can
// disagree with what will actually execute.
func PlatformFor(unameS, unameM string) (Platform, error) {
	var goos string
	switch strings.ToLower(strings.TrimSpace(unameS)) {
	case "linux":
		goos = "linux"
	case "darwin":
		goos = "darwin"
	default:
		return Platform{}, fmt.Errorf(
			"unsupported remote OS %q: quil publishes releases for Linux and macOS", unameS)
	}

	var goarch string
	switch strings.ToLower(strings.TrimSpace(unameM)) {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return Platform{}, fmt.Errorf(
			"unsupported remote architecture %q: quil publishes amd64 and arm64", unameM)
	}

	return Platform{GOOS: goos, GOARCH: goarch}, nil
}
