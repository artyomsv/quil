//go:build windows

package pty

import (
	"testing"
)

// TestWinSession_SetCWD_StoresField verifies SetCWD writes the dir into
// the unexported cwd field that Start later threads into syscall.ProcAttr.
// Live ConPTY spawning is intentionally out of scope here — we want a
// trivial test that runs on every Windows-tagged build without requiring
// a real shell process.
func TestWinSession_SetCWD_StoresField(t *testing.T) {
	s := New().(*winSession)

	if s.cwd != "" {
		t.Errorf("fresh winSession cwd = %q, want empty", s.cwd)
	}

	s.SetCWD(`C:\Users\Public`)
	if s.cwd != `C:\Users\Public` {
		t.Errorf("after SetCWD: cwd = %q, want C:\\Users\\Public", s.cwd)
	}

	// Subsequent SetCWD must overwrite.
	s.SetCWD(`C:\Windows`)
	if s.cwd != `C:\Windows` {
		t.Errorf("second SetCWD: cwd = %q, want C:\\Windows", s.cwd)
	}

	// Empty path must be storable — clears the override.
	s.SetCWD("")
	if s.cwd != "" {
		t.Errorf("SetCWD(\"\"): cwd = %q, want empty", s.cwd)
	}
}

// TestWinSession_SetEnv_StoresField verifies SetEnv stores the slice that
// Start later folds into syscall.ProcAttr.Env. SetCWD's neighbour on the
// same setter pattern — both feed Spawn's process attributes.
func TestWinSession_SetEnv_StoresField(t *testing.T) {
	s := New().(*winSession)

	want := []string{"FOO=bar", "BAZ=qux"}
	s.SetEnv(want)

	if len(s.env) != len(want) {
		t.Fatalf("env len = %d, want %d", len(s.env), len(want))
	}
	for i, v := range want {
		if s.env[i] != v {
			t.Errorf("env[%d] = %q, want %q", i, s.env[i], v)
		}
	}
}

// TestWinSession_NewWithSize_AppliesDimensions confirms the constructor
// honors the cols/rows arguments instead of always defaulting to 80x24.
// A regression here would silently launch shells at the wrong size on
// reconnect, breaking layout-sensitive TUIs.
func TestWinSession_NewWithSize_AppliesDimensions(t *testing.T) {
	s := newWithSize(120, 40).(*winSession)
	if s.cols != 120 || s.rows != 40 {
		t.Errorf("newWithSize(120,40): cols=%d rows=%d, want 120/40", s.cols, s.rows)
	}
	// Default constructor falls back to 80x24.
	d := New().(*winSession)
	if d.cols != 80 || d.rows != 24 {
		t.Errorf("New(): cols=%d rows=%d, want 80/24", d.cols, d.rows)
	}
}

// TestWinSession_PreStart_NoCpty_ReadWriteSafe ensures Read/Write before
// Start return sensible errors instead of panicking on a nil cpty
// pointer. The TUI code occasionally probes panes that haven't spawned
// yet during the spawn-error path.
func TestWinSession_PreStart_NoCpty_ReadWriteSafe(t *testing.T) {
	s := New()

	if _, err := s.Read(make([]byte, 16)); err == nil {
		t.Errorf("Read before Start: err = nil, want non-nil")
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Errorf("Write before Start: err = nil, want non-nil")
	}
	// Resize before Start is a no-op — must not error or panic.
	if err := s.Resize(24, 80); err != nil {
		t.Errorf("Resize before Start: err = %v, want nil", err)
	}
	// Close before Start is a no-op.
	if err := s.Close(); err != nil {
		t.Errorf("Close before Start: err = %v, want nil", err)
	}
}

// TestWinSession_WaitExit_NoHandle_ReturnsMinusOne ensures WaitExit on a
// session that never spawned returns -1 (the sentinel exitCode set in the
// constructor) instead of blocking on a zero handle.
func TestWinSession_WaitExit_NoHandle_ReturnsMinusOne(t *testing.T) {
	s := New().(*winSession)
	if got := s.WaitExit(); got != -1 {
		t.Errorf("WaitExit on unspawned session: got %d, want -1", got)
	}
}
