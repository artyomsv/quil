package notify

import (
	"os"
	"path/filepath"
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
		return `"` + helper + `" --scheme "` + opts.Scheme + `" --home "` + home + `" "%1"`
	}
	return `"` + exePath + `" activate "%1"`
}
