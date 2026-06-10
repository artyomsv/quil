//go:build windows

package main

import "testing"

// TestRealConsoleWindow_LiveWin32 exercises the real Win32 plumbing
// (GetConsoleWindow → GetClassNameW → realConsoleWindow) against whatever
// console the test binary is attached to. It is environment-dependent by
// nature, so it asserts only the INVARIANT rather than a fixed value:
//
//	realConsoleWindow() returns non-zero  ⟺  the console window class is
//	"ConsoleWindowClass" (a genuine conhost window).
//
// Run inside a ConPTY host (Windows Terminal / VS Code) the class is
// "PseudoConsoleWindow" and realConsoleWindow() must return 0 — that is the
// exact condition the first (IsWindowVisible-based) fix failed to detect. The
// t.Log lines make the live values visible when run with -test.v.
func TestRealConsoleWindow_LiveWin32(t *testing.T) {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		t.Skip("no console window attached (headless) — nothing to verify")
	}
	class := windowClassName(hwnd)
	got := realConsoleWindow()
	t.Logf("GetConsoleWindow=0x%X class=%q realConsoleWindow=0x%X", hwnd, class, got)

	wantReal := isRealConsoleClass(class)
	if wantReal && got == 0 {
		t.Errorf("class %q is a real conhost window but realConsoleWindow() returned 0", class)
	}
	if !wantReal && got != 0 {
		t.Errorf("class %q is a ConPTY ghost but realConsoleWindow() returned 0x%X (must be 0 — this is the v1.18.2 bug)", class, got)
	}
}

// TestIsRealConsoleClass guards the discriminator the first ConPTY-ghost fix
// got wrong. The earlier version gated on IsWindowVisible, which is true for
// the ghost, so the guard never fired in a real Windows Terminal session and
// the desktop-blocking maximize still happened. The class name is the reliable
// signal: only a genuine conhost window may be moved/maximized/persisted.
func TestIsRealConsoleClass(t *testing.T) {
	tests := []struct {
		name  string
		class string
		want  bool
	}{
		{"real conhost", "ConsoleWindowClass", true},
		{"conpty ghost (Windows Terminal, VS Code)", "PseudoConsoleWindow", false},
		{"empty (GetClassName failed)", "", false},
		{"unrelated window class", "Chrome_WidgetWin_1", false},
		{"case sensitive — not a match", "consolewindowclass", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRealConsoleClass(tt.class); got != tt.want {
				t.Errorf("isRealConsoleClass(%q) = %v, want %v", tt.class, got, tt.want)
			}
		})
	}
}
