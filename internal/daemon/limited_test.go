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
