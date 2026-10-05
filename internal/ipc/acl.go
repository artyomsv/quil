package ipc

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ownerOnlySDDL is a PROTECTED DACL (no inherited entries) giving full access
// to the owner's SID and LocalSystem only. Administrators are deliberately not
// granted: one can take ownership regardless, and an entry would widen access
// for every elevated process. The inherit form (OICI) is for a directory:
// every file created inside afterwards inherits it AT CREATION, which is what
// covers a temp file or a rotated log that no explicit ACL call ever touches.
// Pure, so it is tested on Linux.
func ownerOnlySDDL(sid string, inherit bool) string {
	flags := ""
	if inherit {
		flags = "OICI"
	}
	return "D:P(A;" + flags + ";FA;;;" + sid + ")(A;" + flags + ";FA;;;SY)"
}

// nullDACL is what foreignAllowSIDs reports for a DACL that is NULL or
// absent: either one lets every principal in, so it is never owner-only.
const nullDACL = "NO_ACCESS_CONTROL"

// aceRe matches one SDDL ACE: (type;flags;rights;object;inherit_object;sid).
var aceRe = regexp.MustCompile(`\(([^;()]*);([^;()]*);([^;()]*);([^;()]*);([^;()]*);([^()]*)\)`)

// foreignAllowSIDs lists, in order, the SIDs of the ALLOW entries in sddl's
// DACL that own does not accept. Deny entries are ignored: they take access
// away. own decides by SID VALUE on Windows, because the same SID can render
// as a string or as an alias (S-1-5-18 and SY, an RID-500 account and LA).
// sddl is expected to carry the DACL only (DACL_SECURITY_INFORMATION).
func foreignAllowSIDs(sddl string, own func(sid string) bool) []string {
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return []string{nullDACL}
	}
	dacl := sddl[i+2:]
	head := dacl
	if j := strings.IndexByte(dacl, '('); j >= 0 {
		head = dacl[:j]
	}
	if strings.Contains(head, nullDACL) {
		return []string{nullDACL}
	}
	var out []string
	for _, m := range aceRe.FindAllStringSubmatch(dacl, -1) {
		if (m[1] == "A" || m[1] == "OA") && !own(m[6]) {
			out = append(out, m[6])
		}
	}
	return out
}

// ownerOnlyDACL reports whether sddl's DACL is already what ProtectDir would
// write: PROTECTED (P in the D: head), at least one entry, every entry an
// ALLOW of full access (FA) inherited by files and folders (exactly OI and
// CI) for a SID own accepts, and at least one of them for user. Anything
// else — a NULL DACL, an inherited, deny, partial or object entry, a SID own
// does not accept, text that is not an entry — reads false, so the caller
// writes. own and user decide by SID value on Windows, like foreignAllowSIDs.
func ownerOnlyDACL(sddl string, own, user func(sid string) bool) bool {
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return false
	}
	dacl := sddl[i+2:]
	j := strings.IndexByte(dacl, '(')
	if j < 0 {
		return false
	}
	head := dacl[:j]
	if strings.Contains(head, nullDACL) || !strings.Contains(head, "P") {
		return false
	}
	aces := dacl[j:]
	hasUser := false
	for aces != "" {
		loc := aceRe.FindStringSubmatchIndex(aces)
		if loc == nil || loc[0] != 0 {
			return false
		}
		m := aceRe.FindStringSubmatch(aces[:loc[1]])
		if m[1] != "A" || !inheritsToAll(m[2]) || m[3] != "FA" || m[4] != "" || m[5] != "" || !own(m[6]) {
			return false
		}
		if user(m[6]) {
			hasUser = true
		}
		aces = aces[loc[1]:]
	}
	return hasUser
}

// inheritsToAll reports whether an ACE's flags are exactly OI and CI, in
// either order: no ID (inherited), IO, NP or audit flag.
func inheritsToAll(flags string) bool {
	return flags == "OICI" || flags == "CIOI"
}

// ErrNotQuilHome is ProtectDir's refusal to rewrite the access list of a
// folder that holds files quil did not write.
var ErrNotQuilHome = errors.New("not a quil folder")

// quilHomeNames are the top-level folders quil writes in QUIL_HOME.
var quilHomeNames = map[string]bool{
	"buffers": true, "claudehook": true, "codexhook": true, "events": true,
	"history": true, "mcp-logs": true, "notes": true, "notes-conflicts": true,
	"opencodehook": true, "paste": true, "plugins": true, "sandbox": true,
	"sessions": true, "shellinit": true, "update": true,
}

// quilHomeStems are the stems of the files quil writes in QUIL_HOME. A name
// that is a stem, or a stem followed by "." or "-", is quil's: that covers
// the extension, a .tmp or .bak beside it, a rotated log and a
// per-destination copy (recent-cwds-<key>.json).
var quilHomeStems = []string{
	"OpenConsole", "audit", "bindings", "config", "conpty",
	"hook", "instances", "notify-activate", "project-groups", "quil", "quild",
	"recent-cwds", "remote-projects", "sandbox-image", "shared-import",
	"templates", "tokens", "web", "window", "workspace",
}

// foreignHomeEntry returns the first name in names that quil does not
// write in QUIL_HOME, or "" when every one is quil's (or there are none).
// It is how ProtectDir tells a quil folder from an arbitrary one that
// QUIL_HOME was pointed at: an arbitrary folder keeps the files it held
// before quil arrived, so it reads foreign on every start, and a real quil
// folder — fresh, or holding only what the TUI and the daemon wrote before
// ProtectDir runs (quild.lock, quild.pid, the logs, conpty.dll) — does not.
// Marker files cannot decide this: quild writes quild.pid before ProtectDir
// runs and shellinit/ and workspace.json after it whatever it decided, so
// any folder would carry them from its second start on. A name missing from
// this list costs a folder that is not already owner-only its rewrite and
// the TCP listener, and quild.log names the entry.
func foreignHomeEntry(names []string) string {
	for _, n := range names {
		if !IsQuilHomeEntry(n) {
			return n
		}
	}
	return ""
}

// IsQuilHomeEntry reports whether name, a top-level entry of QUIL_HOME, is
// one quil writes. Any dot-prefixed name counts: quil's temp files are
// dot-prefixed (.templates-*, .quil-staging-*) and a crash can leave one,
// while a folder QUIL_HOME was pointed at always holds non-dot entries too,
// so detection loses nothing. Exported for the drift test that checks every
// config path against it.
func IsQuilHomeEntry(name string) bool {
	if strings.HasPrefix(name, ".") || quilHomeNames[name] {
		return true
	}
	for _, s := range quilHomeStems {
		if name == s || strings.HasPrefix(name, s+".") || strings.HasPrefix(name, s+"-") {
			return true
		}
	}
	return false
}

// checkQuilHome refuses dir with ErrNotQuilHome when it holds an entry quil
// did not write. A folder that cannot be listed is refused too: ProtectDir
// cannot tell what it would rewrite.
func checkQuilHome(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	if n := foreignHomeEntry(names); n != "" {
		return fmt.Errorf("%w: %s holds %q, which quil did not write, so its access list was left unchanged; move or remove %q from %s, or point QUIL_HOME at a folder of its own", ErrNotQuilHome, dir, n, n, dir)
	}
	return nil
}

// socketACLFailure decides whether a socket whose own owner-only ACL could
// not be applied may still be served. It may only when its directory was
// read back and found owner-only: then the directory is the guard. When the
// directory is open to others too, or could not be read, the two guards
// would fail open together, so this returns the error that stops the
// daemon. Called only after aclErr != nil.
func socketACLFailure(path string, aclErr error, dirWarning string, dirErr error) error {
	switch {
	case dirErr != nil:
		return fmt.Errorf("restrict socket %s to its owner: %v; and its directory's access list could not be read: %w", path, aclErr, dirErr)
	case dirWarning != "":
		return fmt.Errorf("restrict socket %s to its owner: %w; and the directory is not owner-only: %s", path, aclErr, dirWarning)
	}
	return nil
}
