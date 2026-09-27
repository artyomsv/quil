package winjob

import "strings"

// WTS_CONNECTSTATE_CLASS values, mirrored from x/sys/windows so the check
// compiles and is tested on Linux CI. TestJobConstants_MatchXSys pins them on
// Windows.
const (
	wtsStateActive       = 0 // WTSActive
	wtsStateDisconnected = 4 // WTSDisconnected
)

// sessionQualifies reports whether a session is a desktop session of the given
// account: not session 0, Active (which includes a locked screen) or
// Disconnected, and both user name and domain match, case-insensitively.
func sessionQualifies(id, state uint32, name, dom, account, accountDomain string) bool {
	if id == 0 || (state != wtsStateActive && state != wtsStateDisconnected) {
		return false
	}
	return strings.EqualFold(name, account) && strings.EqualFold(dom, accountDomain)
}
