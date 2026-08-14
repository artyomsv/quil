// Package notify raises operating-system notifications for pane attention
// events and routes their activation back into the running TUI.
//
// The package is split so that every file carrying logic is platform-neutral
// and every platform-specific file is close to logic-free. CI builds only
// Linux, so a branch behind //go:build windows is never exercised by
// `dev.sh test` — the same gap that let a statically-dead assignment in
// browse_roots.go pass every test in the tree.
package notify

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// ErrBadURI rejects anything that is not a well-formed activation URI for the
// expected scheme.
var ErrBadURI = errors.New("notify: malformed activation URI")

// activateHost is the single URI host this package will act on. Parsing is
// pinned to it so that adding a second verb later is a deliberate act rather
// than something a caller can reach by guessing.
const activateHost = "activate"

// BuildActivateURI renders the toast's launch target.
//
// The PID is what makes activation address the client that RAISED the toast
// rather than every client attached to the daemon. Several TUIs can be running
// against one workspace, and the toast belongs to exactly one of them.
func BuildActivateURI(scheme string, pid int, paneID string) string {
	q := url.Values{}
	q.Set("pid", strconv.Itoa(pid))
	q.Set("pane", paneID)
	return scheme + "://" + activateHost + "?" + q.Encode()
}

// ParseActivateURI validates a URI handed to us by the operating system.
//
// Registering a URI scheme makes this reachable by ANY local process, and by a
// web page behind a browser confirmation — so this is a trust boundary, not a
// formality. Everything it can produce is a PID and a pane ID that already
// passed ValidPaneID; there is deliberately no path from here to a command, an
// argument list or a filesystem path.
func ParseActivateURI(scheme, raw string) (pid int, paneID string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %v", ErrBadURI, err)
	}
	if u.Scheme != scheme {
		return 0, "", fmt.Errorf("%w: scheme %q, want %q", ErrBadURI, u.Scheme, scheme)
	}
	if u.Host != activateHost {
		return 0, "", fmt.Errorf("%w: host %q, want %q", ErrBadURI, u.Host, activateHost)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %v", ErrBadURI, err)
	}
	// Repeated keys are refused rather than resolved by taking the first or
	// last: a caller sending pane= twice is not a caller we are willing to
	// guess for, and url.Values silently keeps both.
	if len(q["pid"]) != 1 || len(q["pane"]) != 1 {
		return 0, "", fmt.Errorf("%w: pid and pane must each appear exactly once", ErrBadURI)
	}
	pid, err = strconv.Atoi(q.Get("pid"))
	if err != nil || pid <= 0 {
		return 0, "", fmt.Errorf("%w: bad pid %q", ErrBadURI, q.Get("pid"))
	}
	paneID = q.Get("pane")
	if !ValidPaneID(paneID) {
		return 0, "", fmt.Errorf("%w: bad pane id", ErrBadURI)
	}
	return pid, paneID, nil
}

// ValidPaneID enforces the daemon's own pane-id shape: "pane-" plus exactly
// eight lowercase hex digits (see isValidHexID, internal/daemon/daemon.go).
//
// Deliberately the STRICT format rather than hookevents.safePaneID's weaker
// "cannot escape a filepath.Join" rule. That rule exists to protect a path
// join; this one is the whole guarantee that a registry-reachable URI cannot
// name anything but a pane.
func ValidPaneID(id string) bool {
	const prefix = "pane-"
	const hexLen = 8
	if len(id) != len(prefix)+hexLen {
		return false
	}
	if id[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
