package ipc

import "regexp"

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

// aceRe matches one SDDL ACE: (type;flags;rights;object;inherit_object;sid).
var aceRe = regexp.MustCompile(`\(([^;()]*);([^;()]*);([^;()]*);([^;()]*);([^;()]*);([^()]*)\)`)

// foreignAllowSIDs lists the SIDs of ALLOW entries in sddl other than
// allowed, in order. Deny entries are ignored: they take access away.
func foreignAllowSIDs(sddl string, allowed ...string) []string {
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	var out []string
	for _, m := range aceRe.FindAllStringSubmatch(sddl, -1) {
		if (m[1] == "A" || m[1] == "OA") && !ok[m[6]] {
			out = append(out, m[6])
		}
	}
	return out
}
