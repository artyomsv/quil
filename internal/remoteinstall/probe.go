package remoteinstall

import (
	"fmt"
	"strings"
)

// probeLines is how many lines remote-probe.sh contractually prints. Anything
// after them is tolerated: a remote rc file or MOTD can append to stdout, and
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
	// written by the connecting user — the difference between upgrading in
	// place and falling back to ~/.local/bin.
	ExistingDirWritable bool
}

// ParseProbe reads remote-probe.sh's output.
//
// The five-line contract exists so parsing cannot be confused by a host that
// prints a banner: position is fixed, and everything past line five is ignored.
func ParseProbe(out string) (Probe, error) {
	lines := strings.Split(out, "\n")
	for i := range lines {
		// Our own script is LF — pinned by a test — but the remote shell's
		// output is not under our control.
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if len(lines) < probeLines {
		return Probe{}, fmt.Errorf(
			"malformed probe output: got %d lines, want at least %d", len(lines), probeLines)
	}

	home := strings.TrimSpace(lines[0])
	// An unset HOME leaves no way to site the fallback directory: the target
	// would resolve to "/.local/bin" at the filesystem root, which either fails
	// on permissions or — as root — succeeds in the wrong place.
	if home == "" {
		return Probe{}, fmt.Errorf("remote HOME is empty: cannot choose an install directory")
	}
	if !strings.HasPrefix(home, "/") {
		return Probe{}, fmt.Errorf("remote HOME %q is not an absolute path", home)
	}

	platform, err := PlatformFor(lines[1], lines[2])
	if err != nil {
		return Probe{}, err
	}

	p := Probe{Home: home, Platform: platform}
	if existing := strings.TrimSpace(lines[3]); existing != "-" && existing != "" {
		p.ExistingPath = existing
		// Only the literal "rw" grants in-place replacement. A probe that could
		// not answer must never be read as permission to write.
		p.ExistingDirWritable = strings.TrimSpace(lines[4]) == "rw"
	}
	return p, nil
}
