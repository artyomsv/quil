package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func osc7BEL(uri string) string { return "\x1b]7;" + uri + "\x07" }
func osc7ST(uri string) string  { return "\x1b]7;" + uri + "\x1b\\" }

func TestLastOSC7(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"none":           {"plain output", ""},
		"BEL":            {"a" + osc7BEL("file://h/one") + "b", "file://h/one"},
		"ST":             {osc7ST("file://h/two"), "file://h/two"},
		"last wins":      {osc7BEL("file://h/one") + "x" + osc7ST("file://h/two"), "file://h/two"},
		"split at end":   {osc7BEL("file://h/one") + "\x1b]7;file://h/tw", "file://h/one"},
		"bare ESC split": {"\x1b]7;file://h/x\x1b", ""},
	}
	for name, c := range cases {
		if got := lastOSC7([]byte(c.in)); got != c.want {
			t.Errorf("%s: lastOSC7 = %q, want %q", name, got, c.want)
		}
	}
}

// osc7Path must agree with the TUI's parseOSC7Path, which still reports
// every `cd`, or the two would flip the stored value back and forth.
func TestOSC7Path(t *testing.T) {
	cases := map[string]string{
		"file://host/home/me/repo": "/home/me/repo",
		"file:///C:/Users/me":      "C:/Users/me",
		"file://h/with%20space":    "/with space",
		"https://host/path":        "",
		"/not/a/uri":               "",
	}
	for in, want := range cases {
		if got := osc7Path(in); got != want {
			t.Errorf("osc7Path(%q) = %q, want %q", in, got, want)
		}
	}
}

// Through the output pipeline, as a shell's prompt prints it: the flush is
// the on-switch, so the test drives it rather than the detector.
func TestFlushPaneOutput_OSC7UpdatesPaneCWD(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/spawned/here")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	d.flushPaneOutput(pane.ID, []byte("$ cd repo-b\r\n"+osc7ST("file://host/work/repo-b")+"$ "))
	pane.PluginMu.Lock()
	got := pane.CWD
	pane.PluginMu.Unlock()
	if got != "/work/repo-b" {
		t.Fatalf("pane CWD = %q, want /work/repo-b", got)
	}

	// A UNC path a crafted OSC 7 could inject is never stored.
	d.flushPaneOutput(pane.ID, []byte(osc7BEL("file:////evil/share")))
	pane.PluginMu.Lock()
	got = pane.CWD
	pane.PluginMu.Unlock()
	if got != "/work/repo-b" {
		t.Fatalf("pane CWD = %q after a UNC report, want it unchanged", got)
	}
}

// A sandbox pane's agent reports a path inside the container; CWD is the
// host directory and must not take it.
func TestDetectOSC7CWD_SkipsSandboxPane(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "/host/repo")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.PluginMu.Lock()
	pane.ContainerCWD = "/workspace"
	pane.PluginMu.Unlock()
	d.detectOSC7CWD(pane, []byte(osc7BEL("file://box/workspace/sub")))
	pane.PluginMu.Lock()
	got := pane.CWD
	pane.PluginMu.Unlock()
	if got != "/host/repo" {
		t.Fatalf("sandbox pane CWD = %q, want /host/repo", got)
	}
}

func cwdInState(t *testing.T, m *ipc.Message, paneID string) string {
	t.Helper()
	if m.Type != ipc.MsgWorkspaceState {
		return ""
	}
	var s ipc.WorkspaceState
	if err := m.DecodePayload(&s); err != nil {
		t.Fatalf("decode workspace_state: %v", err)
	}
	for _, p := range s.Panes {
		if p.ID == paneID {
			return p.CWD
		}
	}
	return ""
}

// A client that does not parse OSC 7 (the browser, an MCP agent) learns the
// directory from the daemon's broadcast; a TUI's later report of the same
// directory broadcasts nothing more.
func TestOSC7CWD_ReachesEveryClientOnce(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	tab := d.session.CreateTab("T")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	a := attachClientAs(t, sock, "A", 200, 50)
	waitUntil(t, "A attached", func() bool { return d.clientCount() == 1 })

	const dir = "/work/repo-b"
	d.flushPaneOutput(pane.ID, []byte(osc7ST("file://host"+dir)))
	readUntil(t, a, "a state with the new directory", func(m *ipc.Message) bool { return cwdInState(t, m, pane.ID) == dir })

	// The TUI's own report of the same directory: answered, never broadcast.
	sendWithID(t, a, ipc.MsgUpdatePane, "same-cwd", ipc.UpdatePanePayload{PaneID: pane.ID, CWD: dir})
	if got := countType(readUntilID(t, a, ipc.MsgPaneOpResp, "same-cwd", 3*time.Second), ipc.MsgWorkspaceState); got != 0 {
		t.Errorf("an unchanged directory report broadcast %d states, want 0", got)
	}
	// A different directory still does.
	sendWithID(t, a, ipc.MsgUpdatePane, "new-cwd", ipc.UpdatePanePayload{PaneID: pane.ID, CWD: "/work/repo-c"})
	if got := countType(readUntilID(t, a, ipc.MsgPaneOpResp, "new-cwd", 3*time.Second), ipc.MsgWorkspaceState); got != 1 {
		t.Errorf("a changed directory report broadcast %d states, want 1", got)
	}
}
