package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func templateUpdate(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func newTemplateDialog(t *testing.T) Model {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := config.WriteTemplates(config.Templates{Templates: []config.Template{
		{Name: "one", Description: "First description", Panes: []config.TemplatePane{{Type: "terminal"}}},
		{Name: "two", Description: "Second description", Panes: []config.TemplatePane{{Type: "terminal"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.projects[0].RootDir = "/project root"
	m.dialog = dialogCommandPalette
	for _, c := range m.buildPaletteCommands() {
		if c.action == palActNewTemplate {
			m.palette = paletteState{filtered: []paletteCommand{c}}
		}
	}
	if len(m.palette.filtered) != 1 {
		t.Fatal("template palette command missing")
	}
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog != dialogNewTemplate || m.templateUI.row != 0 {
		t.Fatal("dialog did not open on template row")
	}
	return m
}

func TestTemplateDialog_FourRows_SubmitsAllValuesWithTypedSpaces(t *testing.T) {
	for _, branch := range []string{"", "feat/example"} {
		t.Run("branch="+branch, func(t *testing.T) {
			m := newTemplateDialog(t)
			if m.templateUI.cwd != "/project root" {
				t.Fatal("did not start at project root")
			}
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
			if m.templateUI.templates[m.templateUI.selected].Name != "two" || !strings.Contains(m.renderTemplateDialog(), "Second description") {
				t.Fatal("template did not cycle")
			}
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			for _, r := range "A useful task" {
				m = templateUpdate(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m = templateUpdate(t, m, editorPasteMsg("Second line"))
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			m = templateUpdate(t, m, gitReposMsg{Gen: m.repoScan.gen, Resp: ipc.GitReposRespPayload{CWD: m.repoScan.cwd, Repos: []string{"/discovered/one", "/discovered/two"}}})
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
			if m.templateUI.cwd != "/discovered/one" {
				t.Fatal("discovery did not populate directory picker")
			}
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
			if m.templateUI.cwd != "/discovered/two" {
				t.Fatal("reverse directory cycling failed")
			}
			m.templateUI.cwd = ""
			const path = "C:/Program Files/project"
			for _, r := range path {
				m = templateUpdate(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			if m.templateUI.row != 3 {
				t.Fatal("branch is not fourth")
			}
			m = templateUpdate(t, m, tea.PasteMsg{Content: branch})
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			sent := m.client.(*fakeConn).lastSent()
			if sent == nil || sent.Type != ipc.MsgCreateFromTemplateReq {
				t.Fatal("wrong request", sent)
			}
			var req ipc.CreateFromTemplateReqPayload
			if err := sent.DecodePayload(&req); err != nil {
				t.Fatal(err)
			}
			if req.Template != "two" || req.Task != "A useful task\nSecond line" || req.CWD != path || req.Branch != branch || req.ProjectID != "proj-local" {
				t.Fatalf("payload: %+v", req)
			}
			if !m.templateUI.pending {
				t.Fatal("submit did not wait for reply")
			}
		})
	}
}

func TestTemplateDialog_PasteOnEveryRow_OnlyFocusedFieldChangesAndPaneReceivesNothing(t *testing.T) {
	for row := 0; row < 4; row++ {
		for _, transport := range []string{"terminal", "editor", "pending-clipboard"} {
			t.Run(fmt.Sprintf("row%d/%s", row, transport), func(t *testing.T) {
				m := newTemplateDialog(t)
				for i := 0; i < row; i++ {
					m = templateUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
				}
				m.templateUI.cwd = ""
				const text = "paste with spaces\nnext"
				var paste tea.Msg = tea.PasteMsg{Content: text}
				if transport == "editor" {
					paste = editorPasteMsg(text)
				}
				if transport == "pending-clipboard" {
					paste = clipboardPastedMsg{paneID: "pane-1", text: text}
				}
				m = templateUpdate(t, m, paste)
				wantTask, wantDir, wantBranch := "", "", ""
				switch row {
				case 1:
					wantTask = text
				case 2:
					wantDir = sanitizeDialogInput(text)
				case 3:
					wantBranch = sanitizeDialogInput(text)
				}
				f := m.templateUI
				if f.selected != 0 || f.editor.Content() != wantTask || f.cwd != wantDir || f.branch != wantBranch {
					t.Fatalf("paste reached wrong field: task=%q dir=%q branch=%q", f.editor.Content(), f.cwd, f.branch)
				}
				conn := m.client.(*fakeConn)
				conn.mu.Lock()
				defer conn.mu.Unlock()
				for _, msg := range conn.sent {
					if msg.Type == ipc.MsgPaneInput {
						t.Fatal("paste reached live pane")
					}
				}
			})
		}
	}
}

func TestTemplateDialog_RemoteDiscoveryAndSubmit_StayOnPinnedDestination(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local, remote := newFakeConn(), newFakeConn()
	router := NewRouter(map[string]Client{"": local, "gpu01": remote})
	t.Cleanup(func() { router.Remove(""); router.Remove("gpu01") })
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.client = router
	m.projects[1].RootDir, m.activeProject = "/remote root", 1
	next, cmd := m.openNewTemplate()
	m = next.(Model)
	if m.templateUI.dest != "gpu01" {
		t.Fatal("destination not pinned")
	}
	m.activeProject = 0
	runCmd(cmd)
	if local.sentCount() != 0 {
		t.Fatal("remote scan leaked locally")
	}
	sent := remote.lastSent()
	if sent == nil || sent.Type != ipc.MsgGitReposReq || sent.Origin != "gpu01" {
		t.Fatal("scan not stamped", sent)
	}
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if local.sentCount() != 0 || remote.lastSent().Type != ipc.MsgCreateFromTemplateReq {
		t.Fatal("submission changed hosts")
	}
	var req ipc.CreateFromTemplateReqPayload
	if err := remote.lastSent().DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
	if req.ProjectID != "proj-remote" || req.CWD != "/remote root" {
		t.Fatal(req)
	}
}

func TestTemplateDialog_ReplyBeforeOrAfterFrame_FocusesOnlyMatchingRequester(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			m := newTemplateDialog(t)
			m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			reply, err := ipc.NewMessage(ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{TabID: "tab-template", PaneIDs: []string{"p"}})
			if err != nil {
				t.Fatal(err)
			}
			reply.ID = m.templateUI.requestID
			frame := templateWorkspace([]string{"p"}, "p")
			frame.ActiveTab = "tab-1"
			frame.Tabs = append([]TabInfo{{ID: "tab-1", Panes: []string{"pane-1"}}}, frame.Tabs...)
			frame.Panes = append(frame.Panes, PaneInfo{ID: "pane-1", TabID: "tab-1"})
			frame.Projects[0].TabIDs = []string{"tab-1", "tab-template"}
			if !before {
				m = templateUpdate(t, m, frame)
			}
			// Mismatched destination must not accept another daemon's response.
			reply.Origin = "gpu01"
			m = templateUpdate(t, m, templateReplyMsg{reply})
			if !m.templateUI.pending {
				t.Fatal("accepted wrong destination")
			}
			reply.Origin = ""
			conn := m.client.(*fakeConn)
			conn.recv <- reply
			m = templateUpdate(t, m, m.listenForMessages()()) // Exercise wire response dispatch too.
			if before {
				m = templateUpdate(t, m, frame)
			}
			if m.dialog != dialogNone || m.activeTabModel().ID != "tab-template" {
				t.Fatal("requester did not focus returned tab")
			}
			observer := paletteModelWithProjects(t)
			observer = templateUpdate(t, observer, frame)
			if observer.activeTabModel().ID != "tab-1" {
				t.Fatal("broadcast stole another client's focus")
			}
		})
	}
}

func TestTemplateDialog_InvalidConfigOrDaemonRefusal_ShowsErrorAndKeepsDialog(t *testing.T) {
	m := newTemplateDialog(t)
	m = templateUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	reply, err := ipc.NewMessage(ipc.MsgCreateFromTemplateResp, ipc.CreateFromTemplateRespPayload{Error: "directory is missing"})
	if err != nil {
		t.Fatal(err)
	}
	reply.ID = m.templateUI.requestID
	m = templateUpdate(t, m, templateReplyMsg{reply})
	if m.dialog != dialogNewTemplate || m.templateUI.pending || !strings.Contains(m.renderTemplateDialog(), "directory is missing") {
		t.Fatal("refusal not visible")
	}
	if err := os.WriteFile(config.TemplatesPath(), []byte("bad = ["), 0600); err != nil {
		t.Fatal(err)
	}
	next, _ := m.openNewTemplate()
	m = next.(Model)
	if len(m.templateUI.templates) != 0 || !strings.Contains(m.templateUI.err, "Cannot load templates") {
		t.Fatal("invalid config silently replaced")
	}
}
