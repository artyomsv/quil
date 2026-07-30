package transport

import "strings"

// LinkFailure says whether a failed dial is worth retrying.
type LinkFailure int

const (
	// LinkFailureTransient is the default and the safe answer: retry.
	LinkFailureTransient LinkFailure = iota
	// LinkFailurePermanent means further identical attempts cannot succeed.
	LinkFailurePermanent
)

func (f LinkFailure) String() string {
	if f == LinkFailurePermanent {
		return "permanent"
	}
	return "transient"
}

// permanentMarkers are OpenSSH diagnostics that mean an identical retry cannot
// succeed. Deliberately short: every entry is a message whose cause lives in
// configuration or credentials, never in the network.
//
// This project's rule is to detect on the ssh EXIT CODE, never on its prose —
// internal/remoteinstall states it, about "command not found". This is a
// documented exception, for two reasons.
//
// ssh answers 255 for every failure of its own, so a permanent "Permission
// denied" and a transient "Connection timed out" carry the SAME code and the
// rule's preferred signal has no information in it here. And the reason the
// rule exists — locale dependence — does not apply: OpenSSH ships no
// translations for these strings, whereas "command not found" is localised by
// every major shell.
//
// Confirmed against OpenSSH 10.2p1. Re-check when a marker stops firing.
var permanentMarkers = []string{
	"permission denied",                // publickey/password exhausted
	"host key verification failed",     // known_hosts mismatch, or BatchMode refusing the prompt
	"no matching host key type",        // algorithm negotiation — configuration, not weather
	"no matching cipher found",         //
	"no matching mac found",            //
	"too many authentication failures", // the agent offered more keys than sshd accepts
}

// ExitSSHOwnFailure is the status ssh reserves for its own failures — auth,
// host key, DNS, refused connect. The remote shell's codes pass through
// untouched (127, 126, and whatever the command returned), so this value is
// what separates "ssh could not connect" from "the far side had something to
// say". Same signal remoteinstall reads from the other direction.
const ExitSSHOwnFailure = 255

// matchesPermanentMarker reports whether text contains a diagnostic that means
// an identical retry cannot succeed.
//
// Lower-cased before matching. ssh's own casing is stable, but this stream also
// carries the remote command's fd 2 and any server banner, and a case-sensitive
// match would fail silently — the loop simply never parks, with nothing to
// notice.
func matchesPermanentMarker(text string) bool {
	s := strings.ToLower(text)
	for _, m := range permanentMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// ClassifyLinkFailure reports whether a failed link is worth retrying.
//
// stderr alone is NOT sufficient evidence and must never be used alone, which
// is why this takes all three signals rather than exposing the string match.
// ssh multiplexes the REMOTE command's fd 2 onto its own stderr, so the text
// includes whatever the far side's rc files printed — and "permission denied"
// is one of the most common strings any Unix shell emits. Trusting it alone
// lets an unreadable path in someone's ~/.bashrc park the session, and lets a
// compromised remote do it deliberately.
//
// Two independent gates make the text attributable to ssh itself:
//
//   - established — bytes the pump read, which is STDOUT only. Any byte proves
//     the remote command ran, which proves ssh authenticated, which means an
//     auth or host-key marker cannot be ssh's own.
//   - exitCode — 255 is ssh's own failure. If the remote command ran and
//     exited, its status passes through instead. And when ssh is still alive
//     and Close has to kill it, the status is the kill rather than 255 — so a
//     transient drop fails this gate too, which is the safe direction.
//
// The asymmetry is deliberate and load-bearing: anything unproven is
// TRANSIENT. Mis-parking a session that would have healed costs the user their
// session; retrying one that will not costs authentication attempts, which the
// backoff decay already bounds.
func ClassifyLinkFailure(stderr string, established bool, exitCode int) LinkFailure {
	if established || exitCode != ExitSSHOwnFailure {
		return LinkFailureTransient
	}
	if matchesPermanentMarker(stderr) {
		return LinkFailurePermanent
	}
	return LinkFailureTransient
}
