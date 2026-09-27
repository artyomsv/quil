package daemon

import "testing"

// TestBuildWorkspaceState_DaemonLimitedOnlyWhenLimited asserts the broadcast
// state carries "daemon_limited" only for a daemon running in session 0 (see
// Daemon.limited) — a normal daemon must omit the key entirely rather than
// send it false, so an older TUI's parse (absent key) and a current one's
// (explicit false) agree.
func TestBuildWorkspaceState_DaemonLimitedOnlyWhenLimited(t *testing.T) {
	d := newTestDaemon(t)
	if _, ok := d.buildWorkspaceState()["daemon_limited"]; ok {
		t.Error("daemon_limited present on a normal daemon")
	}
	d.limited = true
	if v, _ := d.buildWorkspaceState()["daemon_limited"].(bool); !v {
		t.Error("daemon_limited missing on a limited daemon")
	}
}

// TestNew_InServiceSession_BroadcastsDaemonLimited pins the ON switch: New
// itself must ask inServiceSessionFn, or a session-0 daemon never reports
// [limited] however the state builder behaves.
func TestNew_InServiceSession_BroadcastsDaemonLimited(t *testing.T) {
	prev := inServiceSessionFn
	inServiceSessionFn = func() bool { return true }
	t.Cleanup(func() { inServiceSessionFn = prev })

	d := newTestDaemon(t) // goes through New
	if !d.limited {
		t.Fatal("New did not mark a session-0 daemon limited")
	}
	if v, _ := d.buildWorkspaceState()["daemon_limited"].(bool); !v {
		t.Error("daemon_limited missing from the broadcast state of a session-0 daemon")
	}
}
