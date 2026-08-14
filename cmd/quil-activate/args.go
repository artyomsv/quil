// Deliberately NOT behind //go:build windows, though only the Windows build
// calls it. It is argument parsing — logic, with no syscall in it — and
// internal/notify's standing rule is that logic lives where CI compiles it,
// because CI is Linux and never builds a tagged file. Behind the tag this
// function could not be tested at all; beside it, args_test.go runs on every
// push.
package main

import "strings"

// parseArgs reads the three values `quil notify setup` writes into the registry
// command: the scheme to validate against, the QUIL_HOME to log into, and the
// URI Windows substitutes for %1.
//
// Hand-parsed rather than through the flag package, which prints usage to a
// stderr that does not exist here and calls os.Exit(2) on anything unexpected —
// producing, from a URI handler, exactly the silent failure this whole file is
// about. Unknown flags are ignored for the same reason: an older registry entry
// written by a future version must degrade, not die.
func parseArgs(args []string) (scheme, home, raw string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scheme":
			if i+1 < len(args) {
				i++
				scheme = args[i]
			}
		case "--home":
			if i+1 < len(args) {
				i++
				home = args[i]
			}
		default:
			// An unrecognised FLAG is skipped rather than adopted. The comment
			// above has always claimed unknown flags are ignored; the code did
			// not do it, and took the first one as the URI instead — so a
			// registry entry carrying a flag this build does not know would
			// route the flag and drop the click entirely. Found by the first
			// test ever written for this function.
			//
			// Only the flag token is skipped, not a value that might follow it:
			// nothing here can know whether an unknown flag takes one. That is
			// the honest limit of degrading gracefully against a command we did
			// not write.
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			// The first bare argument is the URI, and parsing STOPS there.
			//
			// That is a security boundary, not tidiness. Windows substitutes
			// the clicked URI for %1, which setup writes as the LAST token, so
			// everything after it was injected by whatever produced the URI. A
			// quote surviving into the URI closes the one around %1 and the
			// remainder becomes fresh argv — and continuing to parse would let
			// it supply its own --home, which activatelog.go turns into an
			// os.OpenFile path. A UNC path there is an outbound SMB connection
			// with implicit authentication, from a click.
			//
			// Every legitimate flag precedes %1 by construction, so stopping
			// costs nothing and the degrade-don't-die intent above is intact.
			raw = args[i]
			return scheme, home, raw
		}
	}
	return scheme, home, raw
}
