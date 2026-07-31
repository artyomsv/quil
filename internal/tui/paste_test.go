package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// pasteTestModel builds a minimal Model with one active terminal pane and the
// supplied recording IPC client — enough to drive the tea.PasteMsg branch in
// Update without a live daemon.
func pasteTestModel(client tuiClient) Model {
	cfg := config.Default()
	pane := NewPaneModel("p1", 1024)
	pane.Active = true
	tab := NewTabModel("t1", "Test")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "p1"
	m := Model{
		cfg:           cfg,
		client:        client,
		tabs:          []*TabModel{tab},
		activeTab:     0,
		width:         80,
		height:        24,
		notifications: NewNotificationCenter(cfg.Notification.SidebarWidth, cfg.Notification.MaxEvents),
	}
	m.resizeTabs()
	return m
}

func paneInputData(t *testing.T, msg *ipc.Message) []byte {
	t.Helper()
	var p ipc.PaneInputPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		t.Fatalf("unmarshal PaneInputPayload: %v", err)
	}
	return p.Data
}

// Test_bracketedPaste covers the pure wrapping helper, in particular the
// paste-injection guard: a payload containing the end marker must not be able
// to close the paste early and smuggle the remainder through as typed input.
func Test_bracketedPaste(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want string
	}{
		{"plain text", "hello world", "\x1b[200~hello world\x1b[201~"},
		{"empty string", "", "\x1b[200~\x1b[201~"},
		{"multi-line", "a\nb", "\x1b[200~a\nb\x1b[201~"},
		{"embedded end marker stripped", "foo\x1b[201~\rrm -rf ~\r", "\x1b[200~foo\rrm -rf ~\r\x1b[201~"},
		{"repeated end markers stripped", "a\x1b[201~b\x1b[201~c", "\x1b[200~abc\x1b[201~"},
		{"start marker passes through", "a\x1b[200~b", "\x1b[200~a\x1b[200~b\x1b[201~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(bracketedPaste(tt.text)); got != tt.want {
				t.Errorf("bracketedPaste(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestUpdate_PasteMsgEmptyContent_FallsBackToImagePaste guards the Ctrl+V
// screenshot-paste regression. Windows Terminal performs its own paste on
// Ctrl+V and delivers it to Quil as a bracketed tea.PasteMsg. For a clipboard
// that holds an image but no text, msg.Content is empty — the old code called
// sendClipboardToPane("") and silently no-oped, so the image proxy never ran
// (only the F8 keypress path had it). An empty bracketed paste must now route
// to the same image-capable path: save the PNG and type its path.
func TestUpdate_PasteMsgEmptyContent_FallsBackToImagePaste(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // PasteDir() writes here, never production

	origText, origImg := clipboardReadText, clipboardReadImage
	t.Cleanup(func() { clipboardReadText, clipboardReadImage = origText, origImg })
	clipboardReadText = func() (string, error) { return "", nil }
	pngBytes := []byte("\x89PNG\r\n\x1a\nFAKE-IMAGE-BYTES")
	clipboardReadImage = func() ([]byte, error) { return pngBytes, nil }

	fake := &fakeSender{}
	m := pasteTestModel(fake)
	// The receiving app (claude-code) has paste mode on, so the typed image
	// path is expected to arrive bracketed.
	m.tabs[0].ActivePaneModel().AppendOutput([]byte("\x1b[?2004h"))

	_, cmd := m.Update(tea.PasteMsg{Content: ""})
	if cmd == nil {
		t.Fatal("empty PasteMsg returned a nil command; want the image-paste command")
	}
	// pasteClipboard sends the input from inside the returned command closure.
	_ = cmd()

	if len(fake.sent) != 1 {
		t.Fatalf("want exactly 1 IPC send (the pasted image path), got %d", len(fake.sent))
	}
	if fake.sent[0].Type != ipc.MsgPaneInput {
		t.Fatalf("sent type = %q, want %q", fake.sent[0].Type, ipc.MsgPaneInput)
	}
	data := string(paneInputData(t, fake.sent[0]))
	if !strings.Contains(data, ".png") {
		t.Errorf("pasted data %q does not contain a .png path", data)
	}
	if !strings.HasPrefix(data, "\x1b[200~") || !strings.HasSuffix(data, "\x1b[201~") {
		t.Errorf("pasted data %q is not wrapped in bracketed paste sequences", data)
	}
}

// TestUpdate_PasteMsgWithText_SendsBracketedPaste guards two things: a
// bracketed paste carrying real text must NOT be hijacked by the image
// fallback, and — when the pane's app has enabled paste mode (?2004) — it
// must be re-wrapped in bracketed paste markers before being injected into
// the pane's PTY. The outer terminal's own \x1b[200~/\x1b[201~ markers
// terminate at Bubble Tea (stripped when building tea.PasteMsg), so without
// re-wrapping the program inside the pane sees the paste as a stream of
// ordinary keystrokes and replays it character by character.
func TestUpdate_PasteMsgWithText_SendsBracketedPaste(t *testing.T) {
	fake := &fakeSender{}
	m := pasteTestModel(fake)
	// Enable ?2004 the way a real app does: through the pane's PTY output
	// stream, exercising the emulator-callback tracking path.
	m.tabs[0].ActivePaneModel().AppendOutput([]byte("\x1b[?2004h"))

	_, _ = m.Update(tea.PasteMsg{Content: "hello world"})

	if len(fake.sent) != 1 {
		t.Fatalf("want exactly 1 IPC send, got %d", len(fake.sent))
	}
	want := "\x1b[200~hello world\x1b[201~"
	if got := string(paneInputData(t, fake.sent[0])); got != want {
		t.Errorf("sent data = %q, want %q", got, want)
	}
}

// TestUpdate_PasteMsgWithText_RawWhenPasteModeOff guards the DECSET 2004 gate:
// an app that never enabled bracketed paste must receive the pasted text as
// raw bytes. Injecting markers it didn't ask for corrupts its stdin — e.g.
// `cat > file` would write the escape bytes into the file.
func TestUpdate_PasteMsgWithText_RawWhenPasteModeOff(t *testing.T) {
	fake := &fakeSender{}
	m := pasteTestModel(fake)

	_, _ = m.Update(tea.PasteMsg{Content: "hello world"})

	if len(fake.sent) != 1 {
		t.Fatalf("want exactly 1 IPC send, got %d", len(fake.sent))
	}
	if got := string(paneInputData(t, fake.sent[0])); got != "hello world" {
		t.Errorf("sent data = %q, want %q", got, "hello world")
	}
}

// TestUpdate_PasteMsg_DaemonAuthoritativePasteMode covers the reattach case:
// the app enabled ?2004 before this client connected, so only the
// daemon-authoritative snapshot flag is set, not the local emulator mirror.
func TestUpdate_PasteMsg_DaemonAuthoritativePasteMode(t *testing.T) {
	fake := &fakeSender{}
	m := pasteTestModel(fake)
	m.tabs[0].ActivePaneModel().daemonBracketedPaste = true

	_, _ = m.Update(tea.PasteMsg{Content: "hi"})

	if len(fake.sent) != 1 {
		t.Fatalf("want exactly 1 IPC send, got %d", len(fake.sent))
	}
	want := "\x1b[200~hi\x1b[201~"
	if got := string(paneInputData(t, fake.sent[0])); got != want {
		t.Errorf("sent data = %q, want %q", got, want)
	}
}

// TestUpdate_PasteMsg_LocalDisableBeatsStaleDaemonFlag pins the precedence
// rule in BracketedPasteEnabled. The daemon flag arrives on the workspace
// snapshot, which is throttled by the mode-broadcast cooldown, so it still
// reads "enabled" for a window after the app emits `CSI ? 2004 l`. If the two
// signals were OR-ed, every paste in that window would inject marker bytes
// into the stdin of an app that just said it does not want them — the exact
// corruption the gate exists to prevent. The local emulator has already seen
// the disable, so it wins.
func TestUpdate_PasteMsg_LocalDisableBeatsStaleDaemonFlag(t *testing.T) {
	fake := &fakeSender{}
	m := pasteTestModel(fake)
	pane := m.tabs[0].ActivePaneModel()
	// Snapshot said enabled; the app has since turned it off, and this client's
	// emulator saw the reset before the next snapshot could correct the mirror.
	pane.daemonBracketedPaste = true
	pane.AppendOutput([]byte("\x1b[?2004h"))
	pane.AppendOutput([]byte("\x1b[?2004l"))

	_, _ = m.Update(tea.PasteMsg{Content: "hello world"})

	if len(fake.sent) != 1 {
		t.Fatalf("want exactly 1 IPC send, got %d", len(fake.sent))
	}
	if got := string(paneInputData(t, fake.sent[0])); got != "hello world" {
		t.Errorf("sent data = %q, want raw %q", got, "hello world")
	}
}

// TestPaneModel_BracketedPasteEnabled_ResetVTFallsBackToDaemon guards the
// other half of the precedence rule: ResetVT installs a fresh emulator that
// has observed nothing, so the pane must fall back to the daemon flag rather
// than latching the pre-reset local value.
func TestPaneModel_BracketedPasteEnabled_ResetVTFallsBackToDaemon(t *testing.T) {
	fake := &fakeSender{}
	m := pasteTestModel(fake)
	pane := m.tabs[0].ActivePaneModel()
	pane.AppendOutput([]byte("\x1b[?2004l"))
	pane.daemonBracketedPaste = true
	if pane.BracketedPasteEnabled() {
		t.Fatal("local disable did not win before ResetVT")
	}

	pane.ResetVT()

	if !pane.BracketedPasteEnabled() {
		t.Error("BracketedPasteEnabled() = false after ResetVT, want the daemon flag to apply again")
	}
}
