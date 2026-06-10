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

// TestUpdate_PasteMsgWithText_SendsTextVerbatim is the regression guard for the
// fix: a bracketed paste carrying real text must still be typed verbatim and
// must NOT be hijacked by the image fallback.
func TestUpdate_PasteMsgWithText_SendsTextVerbatim(t *testing.T) {
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
