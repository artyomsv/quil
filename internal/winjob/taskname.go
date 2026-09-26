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
// The production daemon on its default home gets the plain name. Anything else
// carries the daemon binary name and a short digest of the folded directory, so
// prod, debug and every dev checkout register separate tasks. The directory is
// normalised before hashing — separators unified, trailing separators dropped,
// case folded (Windows paths are case-insensitive) — or `E:\p\.quil` and
// `E:\p\.quil\` would register two tasks and startDaemon would look for the
// wrong one.
func LogonTaskName(daemonName, quilDir string, isDefaultHome bool) string {
	if daemonName == "quild" && isDefaultHome {
		return "Quil daemon"
	}
	norm := strings.ToLower(strings.TrimRight(strings.ReplaceAll(quilDir, "/", `\`), `\`))
	sum := sha256.Sum256([]byte(norm))
	return fmt.Sprintf("Quil daemon (%s %s)", daemonName, hex.EncodeToString(sum[:])[:8])
}
