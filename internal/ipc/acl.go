package ipc

import (
	"fmt"
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
