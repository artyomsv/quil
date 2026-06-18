//go:build windows

package pty

import (
	"log"

	"github.com/artyomsv/quil/internal/pty/winconpty"
	"golang.org/x/sys/windows"
)

// win11Build is the first Windows 11 build number. At or above it the inbox
// conhost is the modern (Terminal-derived) host that renders claude-code's
// incremental input cleanly, so the bundled OpenConsole is unnecessary; below
// it (Windows 10 and older), the inbox conhost mangles the render and we use
// the bundled host instead.
const win11Build = 22000

// PrepareBundledConPTY extracts the bundled OpenConsole host (conpty.dll +
// OpenConsole.exe) under dir on Windows 10 / older, so panes spawn through it
// instead of the buggy inbox conhost. On Windows 11+ it is a no-op (the inbox
// conhost is fine), so the embedded payload is never written or loaded there.
// Best-effort: on failure the PTY layer falls back to the inbox ConPTY.
func PrepareBundledConPTY(dir string) error {
	v := windows.RtlGetVersion()
	if v != nil && v.BuildNumber >= win11Build {
		log.Printf("conpty: Windows build %d (>=11); using inbox conhost", v.BuildNumber)
		return nil
	}
	if v != nil {
		log.Printf("conpty: Windows build %d (<11); extracting bundled OpenConsole", v.BuildNumber)
	}
	return winconpty.Extract(dir)
}
