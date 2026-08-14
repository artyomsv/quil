package notify

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ActivateLogName is the file every toast click writes one line to.
const ActivateLogName = "notify-activate.log"

// maxActivateLogBytes caps that file. It is a diagnostic buffer, not a history:
// one line per click, and the interesting one is always the last.
const maxActivateLogBytes = 64 << 10

// ActivationLogger returns the sink a click handler reports through.
//
// The handler is spawned by Windows, lives for a moment, and — as the windowless
// helper — has no console at all, so a file is the only place it can speak. It
// is also the one component in this feature that could previously fail leaving
// no trace anywhere, which cost several rounds of diagnosis by correlating
// other processes' timestamps by hand.
//
// dir is passed in rather than resolved here: the TUI binary knows QUIL_HOME
// from its own build flags, while the helper is told at registration time and
// has no other way to find it.
//
// Deliberately best-effort and silent on failure. A diagnostic that can break
// an activation is worse than no diagnostic.
func ActivationLogger(dir string) func(string, ...any) {
	return func(format string, args ...any) {
		if dir == "" {
			return
		}
		path := filepath.Join(dir, ActivateLogName)
		// Truncate rather than rotate. Rotation implies someone will read the
		// old file, and nobody will — the failure being diagnosed is always the
		// click that just happened.
		if fi, err := os.Stat(path); err == nil && fi.Size() > maxActivateLogBytes {
			_ = os.Truncate(path, 0)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
}
