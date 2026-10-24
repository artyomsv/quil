package toolupdate

import (
	"path"
	"strings"
)

// unixSystemDirs are the directories a package manager owns on Linux and
// macOS: a binary under one of them was installed by apt/dnf/pacman/snap/nix
// and is updated through that manager, never by a download — overwriting a
// managed file desynchronises the manager's own records. "/usr/lib*" is
// matched by prefix below (lib, lib64, lib32, libexec). "/usr/local" and
// "/opt" are deliberately ABSENT: they are where a manual tarball install
// lands, which is exactly the case the manual update path exists for.
var unixSystemDirs = []string{
	"/usr/bin",
	"/usr/sbin",
	"/usr/libexec",
	"/bin",
	"/sbin",
	"/snap",
	"/nix",
}

// windowsSystemRoots names the environment variables whose directories an
// installer (MSI, winget, the Store) owns on Windows, with the value assumed
// when the variable is unset — a daemon started with a stripped environment
// must not read "Program Files" as a user directory.
var windowsSystemRoots = []struct{ env, fallback string }{
	{"ProgramFiles", `C:\Program Files`},
	{"ProgramFiles(x86)", `C:\Program Files (x86)`},
	{"ProgramW6432", ""},
	{"WINDIR", `C:\Windows`},
}

// windowsAppsSegment is the per-user directory the Microsoft Store and
// MSIX installers populate (…\AppData\Local\Microsoft\WindowsApps). The
// files in it are reparse points onto a package the Store owns, so it is
// refused wherever it appears rather than by a fixed root.
const windowsAppsSegment = "windowsapps"

// SystemLocation reports whether path is inside a directory a package
// manager owns for goos, in which case `quild plugin-update` refuses to
// replace it. env supplies the environment (os.Getenv in production); only
// the windows arm reads it. The comparison is purely textual so the
// decision is testable for either platform on either platform — nothing is
// stat'ed, and a path is examined as given (the daemon already resolved
// symlinks on its side).
func SystemLocation(path, goos string, env func(string) string) bool {
	if goos == "windows" {
		return windowsSystemLocation(path, env)
	}
	return unixSystemLocation(path)
}

func unixSystemLocation(p string) bool {
	p = path.Clean(p)
	if strings.HasPrefix(p, "/usr/lib") {
		return true
	}
	for _, dir := range unixSystemDirs {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func windowsSystemLocation(p string, env func(string) string) bool {
	if env == nil {
		env = func(string) string { return "" }
	}
	norm := normalizeWindows(p)
	for _, seg := range strings.Split(norm, "/") {
		if seg == windowsAppsSegment {
			return true
		}
	}
	for _, root := range windowsSystemRoots {
		dir := env(root.env)
		if dir == "" {
			dir = root.fallback
		}
		if dir == "" {
			continue
		}
		dir = normalizeWindows(dir)
		if norm == dir || strings.HasPrefix(norm, dir+"/") {
			return true
		}
	}
	return false
}

// normalizeWindows lowercases a Windows path and folds both separators to
// "/", with no trailing slash, so prefix comparison is exact on segment
// boundaries. filepath.ToSlash cannot be used: it converts nothing on the
// Linux host CI runs on, and this function must agree on both.
//
// The extended-length prefixes are stripped first: `\\?\C:\Program Files`
// (what EvalSymlinks and long-path-aware tools hand back) and the NT form
// `\??\` name the same directory as `C:\Program Files`, and `\\?\UNC\srv\s`
// is `\\srv\s`. Left in place they would hide a system root behind four
// characters no root list contains.
func normalizeWindows(p string) string {
	p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	switch {
	case strings.HasPrefix(p, "//?/unc/"):
		p = "//" + strings.TrimPrefix(p, "//?/unc/")
	case strings.HasPrefix(p, "//?/"):
		p = strings.TrimPrefix(p, "//?/")
	case strings.HasPrefix(p, "/??/"):
		p = strings.TrimPrefix(p, "/??/")
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return strings.TrimRight(p, "/")
}
