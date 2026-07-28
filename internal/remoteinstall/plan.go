package remoteinstall

// path, not path/filepath: these are always remote POSIX paths, and filepath in
// a Windows build of the TUI would split on backslashes and join with them.
import "path"

// userBinDir is the no-sudo install location, relative to the remote $HOME.
// It matches scripts/install.sh's default so a host provisioned either way
// looks the same.
const userBinDir = ".local/bin"

// Target is where the binaries will be written on the remote host.
type Target struct {
	// Dir is the directory to install into.
	Dir string

	// Shadowed names an existing install this one will take precedence over,
	// or "" when there is none. Non-empty only when an existing install could
	// not be replaced in place, which is worth telling the user: a bare
	// `ssh host quil` would still find the old one.
	Shadowed string
}

// BinaryPath is the absolute path of the installed quil. This is what gets
// persisted per-destination and used verbatim as the ssh remote command.
func (t Target) BinaryPath() string { return path.Join(t.Dir, "quil") }

// PlanTarget picks the install directory.
//
// A fresh install goes to ~/.local/bin: it needs no sudo, and the absolute path
// is persisted afterwards so the non-interactive PATH never has to contain it.
//
// An upgrade replaces the existing binary in place when its directory is
// writable, which keeps a hand-installed /usr/local/bin copy authoritative
// instead of silently leaving two — the stale one still being what a bare
// `ssh host quil` finds. When it is not writable we fall back rather than
// escalate, because a sudo password prompt cannot be answered over a non-tty
// ssh channel, and report the shadowing so the split is visible.
func PlanTarget(p Probe) Target {
	fallback := path.Join(p.Home, userBinDir)
	if p.ExistingPath == "" {
		return Target{Dir: fallback}
	}
	if p.ExistingDirWritable {
		return Target{Dir: path.Dir(p.ExistingPath)}
	}
	return Target{Dir: fallback, Shadowed: p.ExistingPath}
}
