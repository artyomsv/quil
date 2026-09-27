package tui

import (
	"path/filepath"
	"strings"
)

// isWindowsPath reports a drive path (C:\ or C:/) or a UNC path. Pane CWDs
// come from the DAEMON's machine, which need not be this client's OS, so the
// shape of the string — not runtime.GOOS — decides how to split it.
func isWindowsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// displayBase is filepath.Base for a path from any machine: a Windows-shaped
// path splits on both separators (filepath.Base on Linux sees one long name),
// and a drive root is shown as `C:\`.
func displayBase(p string) string {
	if !isWindowsPath(p) {
		return filepath.Base(p)
	}
	t := strings.TrimRight(p, `\/`)
	if len(t) == 2 && t[1] == ':' {
		return t + `\`
	}
	if i := strings.LastIndexAny(t, `\/`); i >= 0 {
		return t[i+1:]
	}
	return t
}
