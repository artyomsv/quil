package remoteinstall

import (
	"fmt"
	"strings"
)

// probeSentinel marks where the probe's own output begins. Everything before it
// belongs to the remote shell's rc files, not to us.
const probeSentinel = "__quil_probe__"

// probeLines is how many lines remote-probe.sh prints after the sentinel.
// Anything further is tolerated: a remote MOTD can append to stdout, and
// refusing to parse because a host is chatty would be a worse failure than the
// one being diagnosed.
const probeLines = 5

// Probe is what the remote host reported about itself.
type Probe struct {
	// Home is the remote $HOME, used to site the ~/.local/bin fallback.
	Home string

	// Platform is the remote's Go build target, already validated against the
	// set quil publishes releases for.
	Platform Platform

	// ExistingPath is the absolute path of an existing quil, or "" when the
	// host has none.
	ExistingPath string

	// ExistingDirWritable reports whether ExistingPath's directory can be
	// written by the connecting user AND is not writable by group or other —
	// the difference between upgrading in place and falling back to
	// ~/.local/bin.
	ExistingDirWritable bool
}

// ParseProbe reads remote-probe.sh's output.
//
// Parsing anchors on the sentinel rather than starting at line 0, because a
// remote shell prints its own output first often enough to matter: ~/.zshenv
// runs for every zsh invocation, ~/.profile may echo, an rc file touching stty
// emits a warning. Without the anchor each of those shifts every field by one
// and produces a confident, wrong diagnosis.
func ParseProbe(out string) (Probe, error) {
	lines := strings.Split(out, "\n")
	for i := range lines {
		// Our own script is LF — pinned by a test — but the remote shell's
		// output is not under our control.
		lines[i] = strings.TrimRight(lines[i], "\r")
	}

	// The LAST sentinel, not the first: a host that echoes its own rc file
	// could otherwise replay a forged one ahead of ours.
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == probeSentinel {
			start = i + 1
		}
	}
	if start < 0 {
		return Probe{}, fmt.Errorf(
			"malformed probe output: no %s marker (did the remote shell fail before running it?)", probeSentinel)
	}
	if len(lines)-start < probeLines {
		return Probe{}, fmt.Errorf(
			"malformed probe output: got %d lines after the marker, want %d", len(lines)-start, probeLines)
	}
	fields := lines[start : start+probeLines]

	home := strings.TrimSpace(fields[0])
	// An unset HOME leaves no way to site the fallback directory: the target
	// would resolve to "/.local/bin" at the filesystem root, which either fails
	// on permissions or — as root — succeeds in the wrong place.
	if home == "" {
		return Probe{}, fmt.Errorf("remote HOME is empty: cannot choose an install directory")
	}
	if err := checkRemotePath("remote HOME", home); err != nil {
		return Probe{}, err
	}

	platform, err := PlatformFor(fields[1], fields[2])
	if err != nil {
		return Probe{}, err
	}

	p := Probe{Home: home, Platform: platform}
	if existing := strings.TrimSpace(fields[3]); existing != "-" && existing != "" {
		// Validated the same way as HOME. `command -v quil` resolves against the
		// remote PATH, which can legitimately contain a relative entry — and a
		// relative result would make the install directory "." and be recorded
		// verbatim as the ssh remote command.
		if err := checkRemotePath("remote quil path", existing); err != nil {
			return Probe{}, err
		}
		p.ExistingPath = existing
		// Only the literal "rw" grants in-place replacement. A probe that could
		// not answer must never be read as permission to write.
		p.ExistingDirWritable = strings.TrimSpace(fields[4]) == "rw"
	}
	return p, nil
}

// checkRemotePath rejects a remote-reported path that is relative or carries
// control characters.
//
// Both matter, and neither is hypothetical. These strings are printed straight
// to the operator's terminal — including in the consent summary, twelve lines
// above the [y/N] prompt — so an escape sequence smuggled through $HOME could
// repaint that block and have the operator approve an install whose displayed
// target, version and shadowing warning are all fabricated. OSC 52 would also
// reach the local clipboard. $HOME is attacker-controlled by anything that can
// write the remote's ~/.bashrc or ~/.ssh/environment, not only by a fully
// compromised host.
//
// Rejecting rather than stripping: no legitimate install path contains a
// control character, and a sanitized path would still be the wrong path.
func checkRemotePath(what, p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s %q is not an absolute path", what, p)
	}
	if i := strings.IndexFunc(p, isControl); i >= 0 {
		return fmt.Errorf("%s contains a control character at byte %d; refusing to use it", what, i)
	}
	return nil
}

// isControl reports the C0, DEL and C1 ranges — everything a terminal acts on —
// plus the bidi overrides and isolates.
//
// Bidi is the non-obvious half, and the reason it belongs in a function named
// for control characters: U+202E is PRINTABLE, so it passes any C0/C1 filter
// while reversing the text after it. internal/tui/remotetext.go exists for the
// same hazard on the render path. It matters more here than there, because
// these paths are printed in the consent summary a dozen lines above the [y/N]
// prompt and in the diagnostics that follow a failed attach — a reversed path
// misrepresents what the operator is approving, and rejecting is right for the
// same reason the control-character check rejects: a sanitized path would still
// be the wrong path.
func isControl(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}
