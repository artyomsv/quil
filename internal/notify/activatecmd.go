package notify

import (
	"os"
	"path/filepath"
	"strings"
)

// ActivateHelperName is the windowless binary that handles a toast click.
//
// Looked for beside the quil binary being registered, so a dev tree and a real
// install each register their own copy rather than whichever happens to be on
// PATH — the same rule findDaemonBinary follows.
const ActivateHelperName = "quil-activate.exe"

// activateCommand builds the registry command a click runs.
//
// Platform-NEUTRAL on purpose, though only Windows calls it: the decision it
// makes is the security-relevant one — the command string is the entire surface
// a registered URI scheme can reach — and everything it does is filepath and
// os.Stat. Behind the //go:build windows tag it would never be compiled by CI,
// which is this package's standing rule and the reason its Windows files hold
// syscalls and nothing else.
//
// PREFERS the windowless helper, falls back to `quil activate` when it is
// absent. The fallback is not decoration: an install upgraded in place may have
// a new quil.exe beside a directory with no helper in it, and a registry entry
// pointing at a file that does not exist is a click that does nothing at all.
// The fallback still routes correctly; it only costs the console flash the
// helper exists to remove.
//
// The helper is told its QUIL_HOME here because it cannot derive one: Windows
// launches it with the environment of the shell rather than of the quil that
// registered it, so a dev build's home would otherwise resolve to the real
// ~/.quil and write its log there.
func activateCommand(opts Options, exePath, home string) string {
	helper := filepath.Join(filepath.Dir(exePath), ActivateHelperName)
	if fi, err := os.Stat(helper); err == nil && !fi.IsDir() {
		return `"` + helper + `" --scheme "` + opts.Scheme + `" --home "` + quotableDir(home) + `" "%1"`
	}
	return `"` + exePath + `" activate "%1"`
}

// quotableDir makes a directory safe to sit inside double quotes on a Windows
// command line.
//
// A TRAILING BACKSLASH is the trap. config.QuilDir returns QUIL_HOME verbatim,
// so `QUIL_HOME=D:\quil\` produces --home "D:\quil\" — and CommandLineToArgvW
// reads \" as an escaped quote, not as a backslash followed by a terminator. The
// value then swallows the rest of the line including the URI, the handler gets
// no URI at all and exits silently, and the log directory it would have
// complained into is garbage too. A click that does nothing and writes nothing,
// from a stray character in an environment variable.
//
// filepath.Clean also normalises the separators and drops any `..`, so the
// registry never carries a path the user cannot read back.
func quotableDir(home string) string {
	if home == "" {
		return ""
	}
	return strings.TrimRight(filepath.Clean(home), `\/`)
}
