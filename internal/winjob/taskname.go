package winjob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// LogonTaskName is the Task Scheduler name for one daemon variant and data
// directory. Both `quil daemon install-logon` and startDaemon compute it, so it
// lives in one function.
//
// Every name carries the daemon binary name and a short digest of the folded
// directory, so prod, debug and every dev checkout register separate tasks —
// and so do two USERS of one PC. Task names are machine-wide: a plain name
// shared by Alice and Bob made Bob's non-elevated `/Create /F` hit Alice's task
// (access denied), and a name-only /Query or /Run from an elevated session
// could pick the other user's task. The default home is per-user, so its
// digest differs per user. The directory is normalised before hashing —
// separators unified, trailing separators dropped, case folded (Windows paths
// are case-insensitive) — or `E:\p\.quil` and `E:\p\.quil\` would register two
// tasks and startDaemon would look for the wrong one.
func LogonTaskName(daemonName, quilDir string) string {
	norm := strings.ToLower(strings.TrimRight(strings.ReplaceAll(quilDir, "/", `\`), `\`))
	sum := sha256.Sum256([]byte(norm))
	return fmt.Sprintf("Quil daemon (%s %s)", daemonName, hex.EncodeToString(sum[:])[:8])
}
