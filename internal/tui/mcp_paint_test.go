package tui

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

func TestUpdate_MCPHiddenPaneDimensions(t *testing.T) {
	for _, dest := range []string{"", "test-remote"} {
		t.Run("dest="+dest, func(t *testing.T) {
			fs := &echoRecorder{}
			// No receive pump: Update is driven synchronously with the same
			// destination-stamped messages the SSH router would deliver.
			router := &Router{conns: map[string]Client{dest: fs}, in: make(chan *ipc.Message, 1)}
			router.SetActiveDest(dest)
			m := Model{cfg: config.Default(), client: router,
				notifications: NewNotificationCenter(30, 50),
				mcpHighlights: make(map[string]bool), tabDragFromIdx: -1,
				sized: true, width: 178, height: 58}
			m.initKeymap()
			state := WorkspaceStateMsg{Dest: dest, ActiveTab: "visible",
				Tabs:  []TabInfo{{ID: "visible", Name: "Visible", Panes: []string{"shell"}}},
				Panes: []PaneInfo{{ID: "shell", TabID: "visible", Cols: 176, Rows: 54}}}
			next, _ := m.Update(state)
			m = next.(Model)
			state.Tabs = append(state.Tabs, TabInfo{ID: "hidden", Name: "Hidden", Panes: []string{"agent"}})
			state.Panes = append(state.Panes, PaneInfo{ID: "agent", TabID: "hidden", Type: "claude-code", Cols: 80, Rows: 24})
			next, cmd := m.Update(state)
			m = next.(Model)
			router.in <- &ipc.Message{Type: "test-inert"}
			runCmd(cmd)
			t.Cleanup(func() {
				for _, tab := range m.allTabs() {
					for _, p := range tab.Leaves() {
						p.Dispose()
					}
				}
			})
			p := m.tabByID("hidden").Leaves()[0]
			if m.activeTabModel().ID != "visible" {
				t.Fatal("MCP creation switched the visible tab")
			}
			resized := false
			for _, msg := range fs.sent {
				if msg.Type != ipc.MsgResizePanes {
					continue
				}
				var batch ipc.ResizePanesPayload
				if err := msg.DecodePayload(&batch); err != nil {
					t.Fatal(err)
				}
				for _, it := range batch.Panes {
					if it.PaneID != p.ID {
						continue
					}
					resized = true
					t.Logf("hidden: broadcast=80x24 emulator=%dx%d resize=%dx%d", p.vt.Width(), p.vt.Height(), it.Cols, it.Rows)
					if p.vt.Width() != int(it.Cols) || p.vt.Height() != int(it.Rows) {
						t.Fatal("emulator/PTY resize split")
					}
				}
			}
			if !resized {
				t.Fatal("Update did not send a resize for the hidden pane")
			}
			next, _ = m.Update(tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt})
			m = next.(Model)
			if m.activeTabModel().ID != "hidden" {
				t.Fatal("tab switch did not reach the MCP pane")
			}
			t.Logf("shown: emulator=%dx%d", p.vt.Width(), p.vt.Height())
			if p.vt.Width() != 176 || p.vt.Height() != 54 {
				t.Fatalf("emulator=%dx%d, want 176x54", p.vt.Width(), p.vt.Height())
			}
			output := func(generation uint64, data string) {
				wire, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: p.ID, Data: []byte(data), Generation: generation})
				if err != nil {
					t.Fatal(err)
				}
				router.in <- wire
				next, _ = m.Update(m.listenForMessages()())
				m = next.(Model)
			}
			output(1, "OLD SCREEN\r\nold input box\x1b[?1000h\x1b[?2004h\x1b]2;unfinished title")
			freshData := "NEW-" + strings.Repeat("x", 169) + "END\r\nprompt"
			output(2, freshData)
			fresh := NewPaneModel("comparison", 1024)
			defer fresh.Dispose()
			fresh.ResizeVT(176, 54)
			fresh.AppendOutput([]byte(freshData))
			if got, want := p.vt.Render(), fresh.vt.Render(); got != want {
				t.Fatalf("restarted pane differs from a fresh pane:\n%s\nwant:\n%s", got, want)
			}
			if p.mouseNormal || p.bracketedPaste {
				t.Fatal("restart retained old terminal modes")
			}
			output(1, "LATE OLD OUTPUT")
			if p.vt.Render() != fresh.vt.Render() {
				t.Fatal("late old output polluted the replacement screen")
			}
		})
	}
}
