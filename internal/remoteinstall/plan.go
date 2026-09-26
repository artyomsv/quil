package remoteinstall

// path, not path/filepath: POSIX remote paths are joined with path, and in a
// Windows build of the TUI filepath would split on backslashes and join with
// them. Windows remote paths are joined by hand (winJoin), for the mirror
// reason — filepath on a Linux TUI would not know a backslash is a separator.
import (
	"path"
	"strings"
)

// userBinDir is the no-sudo install location, relative to the remote $HOME.
// It matches scripts/install.sh's default so a host provisioned either way
// looks the same.
const userBinDir = ".local/bin"

// winProgramsQuil is the fresh-install dir under %LOCALAPPDATA%. The Windows
// probe checks <LOCALAPPDATA>\Programs\quil\quil.exe FIRST, which is what makes
// healRemoteRecord's "recorded == probe.ExistingPath" termination proof hold
// on Windows too.
const winProgramsQuil = `Programs\quil`

// Target is where the binaries will be written on the remote host.
type Target struct {
	// Dir is the directory to install into.
	Dir string

	// Shadowed names an existing install this one will take precedence over,
	// or "" when there is none. Non-empty only when an existing install could
	// not be replaced in place, which is worth telling the user: a bare
	// `ssh host quil` would still find the old one.
	Shadowed string

	// OS is "" for a POSIX host and "windows" for a Windows host, copied from
	// Probe.OS. It selects the path syntax and the installer.
	OS string
}

// BinaryPath is the absolute path of the installed quil. This is what gets
// persisted per-destination and used verbatim as the ssh remote command.
func (t Target) BinaryPath() string {
	if t.OS == "windows" {
		return winJoin(t.Dir, "quil.exe")
	}
	return path.Join(t.Dir, "quil")
}

// PlanTarget picks the install directory.
//
// A fresh install goes to ~/.local/bin: it needs no sudo, and the absolute path
// is persisted afterwards so the non-interactive PATH never has to contain it.
// On Windows the same role is played by %LOCALAPPDATA%\Programs\quil, the
// per-user location that needs no elevation.
//
// An upgrade replaces the existing binary in place when its directory is
// writable, which keeps a hand-installed /usr/local/bin copy authoritative
// instead of silently leaving two — the stale one still being what a bare
// `ssh host quil` finds. When it is not writable we fall back rather than
// escalate, because a sudo password prompt cannot be answered over a non-tty
// ssh channel, and report the shadowing so the split is visible.
func PlanTarget(p Probe) Target {
	if p.OS == "windows" {
		fallback := winJoin(p.Home, winProgramsQuil)
		switch {
		case p.ExistingPath == "":
			return Target{Dir: fallback, OS: "windows"}
		case p.ExistingDirWritable:
			return Target{Dir: winDir(p.ExistingPath), OS: "windows"}
		default:
			return Target{Dir: fallback, Shadowed: p.ExistingPath, OS: "windows"}
		}
	}
	fallback := path.Join(p.Home, userBinDir)
	if p.ExistingPath == "" {
		return Target{Dir: fallback}
	}
	if p.ExistingDirWritable {
		return Target{Dir: path.Dir(p.ExistingPath)}
	}
	return Target{Dir: fallback, Shadowed: p.ExistingPath}
}

// winJoin joins a Windows directory and a name with exactly one backslash, so
// a drive root (`D:\`) does not become `D:\\quil.exe`.
func winJoin(dir, name string) string { return strings.TrimRight(dir, `\`) + `\` + name }

// winDir is the directory of a Windows file path; a drive root keeps its
// separator (`D:\quil.exe` → `D:\`).
func winDir(p string) string {
	i := strings.LastIndex(p, `\`)
	if i < 0 {
		return p
	}
	d := p[:i]
	if len(d) == 2 && d[1] == ':' {
		return d + `\`
	}
	return d
}
