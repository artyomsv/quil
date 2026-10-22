package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// sbxPasteModel is pasteTestModel with its pane running in an sbx sandbox.
func sbxPasteModel(t *testing.T, fake *fakeSender) Model {
	t.Helper()
	origText, origImg := clipboardReadText, clipboardReadImage
	t.Cleanup(func() { clipboardReadText, clipboardReadImage = origText, origImg })
	clipboardReadText = func() (string, error) { return "", nil }
	clipboardReadImage = func() ([]byte, error) { return []byte("\x89PNG\r\n\x1a\nIMG"), nil }
	m := pasteTestModel(fake)
	m.curTabs()[0].ActivePaneModel().SandboxKind = sandboxKindSbx
	return m
}

// An image pasted into an sbx pane goes to the daemon, not to a file on this
// machine, and the path the daemon answers is what gets typed.
func TestSbxImagePaste_SendsTheImageAndTypesTheAnsweredPath(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	fake := &fakeSender{}
	m := sbxPasteModel(t, fake)

	_, cmd := m.Update(tea.PasteMsg{Content: ""})
	msg := cmd()
	sm, ok := msg.(sandboxImagePasteMsg)
	if !ok {
		t.Fatalf("paste produced %T, want sandboxImagePasteMsg", msg)
	}
	next, cmd := m.Update(sm)
	m = next.(Model)
	// The send runs in the command; the timeout tick is not run here.
	if c := cmd; c != nil {
		if batch, ok := c().(tea.BatchMsg); ok && len(batch) > 0 && batch[0] != nil {
			batch[0]()
		}
	}
	if len(fake.sent) != 1 || fake.sent[0].Type != ipc.MsgPasteImageReq || fake.sent[0].ID == "" {
		t.Fatalf("sent = %+v, want one id-bearing paste_image_req", fake.sent)
	}
	var req ipc.PasteImageReqPayload
	if err := fake.sent[0].DecodePayload(&req); err != nil || req.PaneID != "p1" || !strings.HasPrefix(string(req.PNG), "\x89PNG") {
		t.Fatalf("request = %+v, err %v", req, err)
	}

	path := "/tmp/quil-paste-20261022-120000-0011223344556677/image.png"
	next, _ = m.Update(pasteImageRespMsg{Gen: fake.sent[0].ID, Resp: ipc.PasteImageRespPayload{PaneID: "p1", Path: path}})
	m = next.(Model)
	if len(fake.sent) != 2 || fake.sent[1].Type != ipc.MsgPaneInput {
		t.Fatalf("sent = %d frames, want the path typed as pane_input", len(fake.sent))
	}
	if got := string(paneInputData(t, fake.sent[1])); !strings.Contains(got, path) {
		t.Errorf("typed %q, want %q", got, path)
	}
	if len(m.pasteImageReqs) != 0 {
		t.Errorf("request still pending: %v", m.pasteImageReqs)
	}
}

// A path of any other shape is refused rather than typed: the daemon may be
// another machine, and the text goes into a PTY.
func TestSbxImagePaste_RefusesAnUnexpectedPath(t *testing.T) {
	for _, p := range []string{
		"/etc/passwd",
		"/tmp/quil-paste/x.png",
		"/tmp/quil-paste-../image.png",
		"/tmp/quil-paste-20261022-120000-0011223344556677/image.png\rrm -rf ~\r",
		"/tmp/quil-paste-20261022-120000-0011223344556677/sub/image.png",
	} {
		fake := &fakeSender{}
		m := sbxPasteModel(t, fake)
		m.pasteImageReqs = map[string]string{"7": "p1"}
		next, _ := m.Update(pasteImageRespMsg{Gen: "7", Resp: ipc.PasteImageRespPayload{PaneID: "p1", Path: p}})
		m = next.(Model)
		if len(fake.sent) != 0 {
			t.Errorf("path %q was typed", p)
		}
		if !strings.Contains(m.flashText, "unexpected path") {
			t.Errorf("path %q: flash = %q", p, m.flashText)
		}
	}
}

// An error answer is shown, an answer nobody waits for is dropped, and a
// timeout forgets the request so a late answer types nothing.
func TestSbxImagePaste_ErrorsAndLateAnswers(t *testing.T) {
	fake := &fakeSender{}
	m := sbxPasteModel(t, fake)
	m.pasteImageReqs = map[string]string{"1": "p1", "2": "p1"}

	next, _ := m.Update(pasteImageRespMsg{Gen: "1", Resp: ipc.PasteImageRespPayload{Error: "the pane's agent is not running"}})
	m = next.(Model)
	if !strings.Contains(m.flashText, "not running") {
		t.Errorf("flash = %q", m.flashText)
	}

	next, _ = m.Update(pasteImageTimeoutMsg{gen: "2"})
	m = next.(Model)
	next, _ = m.Update(pasteImageRespMsg{Gen: "2", Resp: ipc.PasteImageRespPayload{Path: "/tmp/quil-paste-20261022-120000-0011223344556677/image.png"}})
	m = next.(Model)
	if len(fake.sent) != 0 {
		t.Errorf("a late answer was typed: %+v", fake.sent)
	}
}

// A host pane keeps the old path: the image is saved here and its path typed.
func TestImagePaste_HostPaneStillSavesLocally(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	fake := &fakeSender{}
	m := sbxPasteModel(t, fake)
	m.curTabs()[0].ActivePaneModel().SandboxKind = ""
	_, cmd := m.Update(tea.PasteMsg{Content: ""})
	if msg := cmd(); func() bool { _, ok := msg.(clipboardPastedMsg); return !ok }() {
		t.Fatalf("host pane paste produced %T, want clipboardPastedMsg", msg)
	}
}

// An image over the wire limit is refused before anything is sent.
func TestSbxImagePaste_RefusesAnOversizedImage(t *testing.T) {
	fake := &fakeSender{}
	m := sbxPasteModel(t, fake)
	if cmd := m.startSandboxImagePaste("p1", "", make([]byte, ipc.MaxPasteImageBytes+1)); cmd != nil {
		t.Error("an oversized image produced a send")
	}
	if len(fake.sent) != 0 || len(m.pasteImageReqs) != 0 || !strings.Contains(m.flashText, "too large") {
		t.Errorf("sent=%d pending=%v flash=%q", len(fake.sent), m.pasteImageReqs, m.flashText)
	}
}

// A send that fails on this side is reported, and — being no IPC response —
// does not re-arm the listen loop (a second listener would double-read).
func TestSbxImagePaste_LocalSendFailure(t *testing.T) {
	fake := &fakeSender{}
	m := sbxPasteModel(t, fake)
	m.pasteImageReqs = map[string]string{"9": "p1"}
	next, cmd := m.Update(pasteImageRespMsg{Gen: "9", local: true, Resp: ipc.PasteImageRespPayload{Error: "link lost"}})
	m = next.(Model)
	if cmd != nil {
		t.Error("a local failure returned a command; it must not re-arm the listen loop")
	}
	if !strings.Contains(m.flashText, "link lost") || len(m.pasteImageReqs) != 0 {
		t.Errorf("flash=%q pending=%v", m.flashText, m.pasteImageReqs)
	}
}

// twoHostPasteModel is a workspace on two connected hosts, with the sbx pane
// on the LOCAL one. Returns the model and the two hosts' connections.
func twoHostPasteModel() (Model, *fakeConn, *fakeConn) {
	local, gpu := newFakeConn(), newFakeConn()
	r := NewRouter(map[string]Client{"": local, "gpu01": gpu})
	m := Model{
		client: r,
		projects: []*ProjectModel{
			{ID: "proj-local", Dest: "", tabs: []*TabModel{tabWithPane("tab-1", "pane-local")}},
			{ID: "proj-gpu", Dest: "gpu01", tabs: []*TabModel{tabWithPane("tab-9", "pane-gpu")}},
		},
	}
	m.projects[0].tabs[0].ActivePaneModel().SandboxKind = sandboxKindSbx
	m.syncActiveDest()
	return m, local, gpu
}

// runSend runs the send half of startSandboxImagePaste's command, never its
// timeout tick.
func runSend(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok && len(batch) > 0 && batch[0] != nil {
		batch[0]()
	}
}

// The screenshot is bound to the host its pane was on when the user asked to
// paste. If that pane is gone by the time the clipboard read returns — closed,
// or its host disconnected — and another host is active, the image goes
// NOWHERE: destOfPane's active-host fallback must never carry it. Driven
// through Update, with the clipboard result delivered after the pane left.
func TestSbxImagePaste_PaneGoneBeforeTheReadReturns_SendsToNoHost(t *testing.T) {
	m, local, gpu := twoHostPasteModel()
	delayed := sandboxImagePasteMsg{paneID: "pane-local", dest: "", png: []byte("\x89PNG\r\n\x1a\nIMG")}

	// The pane closes and the user moves to the remote host meanwhile.
	m.projects[0].tabs = nil
	m.activeProject = 1
	m.syncActiveDest()

	next, cmd := m.Update(delayed)
	m = next.(Model)
	runSend(cmd)

	if local.sentCount() != 0 || gpu.sentCount() != 0 {
		t.Fatalf("local=%d gpu=%d sends — an image whose pane is gone must reach no host",
			local.sentCount(), gpu.sentCount())
	}
	if len(m.pasteImageReqs) != 0 || !strings.Contains(m.flashText, "gone") {
		t.Errorf("pending=%v flash=%q", m.pasteImageReqs, m.flashText)
	}
}

// The same pane on a DIFFERENT host than it was bound to (a reconnect handed
// the id to another daemon) is refused too: the bound host is the only one
// the image may reach.
func TestSbxImagePaste_PaneOnAnotherHost_SendsToNoHost(t *testing.T) {
	m, local, gpu := twoHostPasteModel()
	next, cmd := m.Update(sandboxImagePasteMsg{paneID: "pane-local", dest: "gpu01", png: []byte("\x89PNG\r\n\x1a\nIMG")})
	m = next.(Model)
	runSend(cmd)
	if local.sentCount() != 0 || gpu.sentCount() != 0 {
		t.Fatalf("local=%d gpu=%d sends, want none", local.sentCount(), gpu.sentCount())
	}
	if len(m.pasteImageReqs) != 0 {
		t.Errorf("pending=%v", m.pasteImageReqs)
	}
}

// A pane still on its bound host gets its image there, and only there, even
// while the OTHER host's project is the active one.
func TestSbxImagePaste_GoesToTheBoundHostNotTheActiveOne(t *testing.T) {
	m, local, gpu := twoHostPasteModel()
	m.activeProject = 1
	m.syncActiveDest()

	next, cmd := m.Update(sandboxImagePasteMsg{paneID: "pane-local", dest: "", png: []byte("\x89PNG\r\n\x1a\nIMG")})
	m = next.(Model)
	runSend(cmd)

	if local.sentCount() != 1 || gpu.sentCount() != 0 {
		t.Fatalf("local=%d gpu=%d sends, want the image on local only", local.sentCount(), gpu.sentCount())
	}
	if len(m.pasteImageReqs) != 1 {
		t.Errorf("pending=%v, want one request", m.pasteImageReqs)
	}
}
