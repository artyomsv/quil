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

// ClassifyLinkFailure reports whether ssh's stderr describes a failure that
// retrying cannot fix.
//
// The asymmetry is deliberate and load-bearing: anything unmatched is
// TRANSIENT. Mis-parking a session that would have healed is worse than
// retrying one that will not, because the retry costs authentication attempts
// while the mis-park costs the user their session.
//
// Lower-cased before matching. ssh's own casing is stable, but this stream also
// carries the remote command's fd 2 and any server banner, and a case-sensitive
// match would fail silently — the loop simply never parks, with nothing to
// notice.
func ClassifyLinkFailure(stderr string) LinkFailure {
	s := strings.ToLower(stderr)
	for _, m := range permanentMarkers {
		if strings.Contains(s, m) {
			return LinkFailurePermanent
		}
	}
	return LinkFailureTransient
}
