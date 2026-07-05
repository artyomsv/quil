package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestUpdate_MouseWheel_ForwardsToMouseTrackingPane drives the full Update path
// for a wheel event over a pane whose app enabled mouse tracking (exactly the
// sequences opencode emits at startup). The wheel must be forwarded to the PTY
// as an SGR mouse sequence, NOT swallowed into Quil's (empty) scrollback.
func TestUpdate_MouseWheel_ForwardsToMouseTrackingPane(t *testing.T) {
	cfg := config.Default()
	pane := NewPaneModel("oc1", 1024)
	pane.Active = true
	pane.Type = "opencode"
	tab := NewTabModel("t1", "Test")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "oc1"
	fake := &fakeSender{}
	m := Model{
		cfg:           cfg,
		client:        fake,
		tabs:          []*TabModel{tab},
		activeTab:     0,
		width:         80,
		height:        24,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
		mcpHighlights: make(map[string]bool),
	}
	m.resizeTabs()

	// opencode's real startup mouse-enable sequence.
	pane.AppendOutput([]byte("\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h"))
	if !pane.MouseTracking() {
		t.Fatalf("MouseTracking() = false after opencode mouse-enable; flags x10=%v normal=%v button=%v any=%v sgr=%v",
			pane.mouseX10, pane.mouseNormal, pane.mouseButton, pane.mouseAny, pane.mouseSGR)
	}

	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 5})

	if len(fake.sent) == 0 {
		t.Fatal("no IPC send: wheel was not forwarded to the mouse-tracking pane")
	}
	if fake.sent[0].Type != ipc.MsgPaneInput {
		t.Fatalf("sent type = %q, want %q", fake.sent[0].Type, ipc.MsgPaneInput)
	}
	data := string(paneInputData(t, fake.sent[0]))
	if !strings.HasPrefix(data, "\x1b[<") {
		t.Errorf("forwarded data = %q, want an SGR mouse sequence (\\x1b[<...)", data)
	}
	if !strings.HasPrefix(data, "\x1b[<64;") {
		t.Errorf("forwarded data = %q, want wheel-up button 64", data)
	}
}

// TestUpdate_MouseWheel_NonTrackingPaneScrollsLocally guards the regression
// vector the forward branch introduces: it sits as an early return BEFORE the
// pre-existing local-scroll logic in Update, so a pane that never enabled mouse
// tracking (a plain terminal/shell) must NOT forward the wheel to the PTY — it
// must fall through and scroll Quil's own scrollback. A bug that made the
// forward branch fire unconditionally would break normal terminal scrolling and
// is otherwise untested.
func TestUpdate_MouseWheel_NonTrackingPaneScrollsLocally(t *testing.T) {
	cfg := config.Default()
	pane := NewPaneModel("sh1", 4096)
	pane.Active = true
	pane.Type = "terminal"
	tab := NewTabModel("t1", "Test")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "sh1"
	fake := &fakeSender{}
	m := Model{
		cfg:           cfg,
		client:        fake,
		tabs:          []*TabModel{tab},
		activeTab:     0,
		width:         80,
		height:        24,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
		mcpHighlights: make(map[string]bool),
	}
	m.resizeTabs()

	// Fill the scrollback so an upward wheel has somewhere to scroll into. No
	// mouse-enable sequence is emitted, so MouseTracking() must stay false.
	for i := 0; i < 200; i++ {
		pane.AppendOutput([]byte("line\r\n"))
	}
	if pane.MouseTracking() {
		t.Fatal("MouseTracking() = true for a plain terminal pane, want false")
	}

	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: 5})

	if len(fake.sent) != 0 {
		t.Fatalf("wheel forwarded to PTY for a non-tracking pane: %d IPC send(s)", len(fake.sent))
	}
	if pane.scrollBack == 0 {
		t.Error("scrollBack = 0 after wheel-up on a non-tracking pane; expected local scroll")
	}
}

// TestUpdate_MouseWheel_ForwardsViaDaemonFlagOnReattach is the regression guard
// for the real-world failure: when the TUI attaches to an already-running
// opencode, the local emulator never sees the one-time mouse-enable burst, so
// the ONLY signal is the daemon-authoritative flag delivered in the workspace
// snapshot (mirrored onto PaneModel.daemonMouseTracking). The wheel must still
// forward, encoded as SGR.
func TestUpdate_MouseWheel_ForwardsViaDaemonFlagOnReattach(t *testing.T) {
	cfg := config.Default()
	pane := NewPaneModel("oc2", 1024)
	pane.Active = true
	pane.Type = "opencode"
	// Simulate the daemon snapshot reconciliation: no local emulator modes were
	// ever observed (reattach), only the daemon flags.
	syncPaneMeta(pane, &PaneInfo{ID: "oc2", Type: "opencode", MouseTracking: true, MouseSGR: true}, false)
	if !pane.MouseTracking() {
		t.Fatal("MouseTracking() = false with daemon flag set, want true")
	}

	tab := NewTabModel("t1", "Test")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "oc2"
	fake := &fakeSender{}
	m := Model{
		cfg:           cfg,
		client:        fake,
		tabs:          []*TabModel{tab},
		activeTab:     0,
		width:         80,
		height:        24,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
		mcpHighlights: make(map[string]bool),
	}
	m.resizeTabs()

	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: 5})

	if len(fake.sent) == 0 {
		t.Fatal("no IPC send: wheel not forwarded when only the daemon flag is set")
	}
	data := string(paneInputData(t, fake.sent[0]))
	if !strings.HasPrefix(data, "\x1b[<65;") {
		t.Errorf("forwarded data = %q, want SGR wheel-down (button 65)", data)
	}
}
