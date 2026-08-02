package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// pluginClientModel builds a minimal Model with a fake IPC client and a real
// plugin registry, local mode by default (no SetRemoteDest).
func pluginClientModel(t *testing.T) *Model {
	t.Helper()
	return &Model{
		client:         &fakeSender{},
		pluginRegistry: plugin.NewRegistry(),
	}
}

func TestApplyPluginList_AdoptsTheServersAnswer(t *testing.T) {
	m := pluginClientModel(t)
	m.applyPluginList(ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})
	if m.pluginRegistry.Get("terminal").Available {
		t.Error("Available = true, want false — the server's answer was ignored")
	}
}

// An empty answer is not a statement that nothing is installed; it is a daemon
// that answered nothing useful. Keeping the local values fails toward the loud
// error (a wrong offer fails at spawn) instead of the silent one (a wrong
// grey-out hides a working tool).
func TestApplyPluginList_EmptyAnswerKeepsLocalDetection(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()
	m.applyPluginList(ipc.PluginListRespPayload{})
	if !m.pluginRegistry.Get("terminal").Available {
		t.Error("Available = false — an empty answer overwrote local detection")
	}
}

// Local mode must not adopt the daemon's answer: it detects at start and runs
// for weeks, while this TUI detected at launch. Adopting it would grey out a
// tool installed since the daemon booted — a regression for every local user.
func TestRequestPluginList_LocalModeAsksNothing(t *testing.T) {
	m := pluginClientModel(t) // no SetRemoteDest
	if cmd := m.requestPluginList(); cmd != nil {
		runCmd(cmd)
		t.Fatalf("local mode returned a command; %d messages sent, want no command at all",
			len(m.client.(*fakeSender).sent))
	}
}

func TestRequestPluginList_RemoteModeAsks(t *testing.T) {
	m := pluginClientModel(t)
	m.asRemote("gpu01")
	cmd := m.requestPluginList()
	if cmd == nil {
		t.Fatal("requestPluginList returned no command in remote mode")
	}
	runCmd(cmd)
	sent := m.client.(*fakeSender).sent
	if len(sent) != 1 || sent[0].Type != ipc.MsgPluginListReq {
		t.Errorf("sent = %+v, want one %s", sent, ipc.MsgPluginListReq)
	}
}

// A nil registry must not panic: applyPluginList can run before NewModel has
// wired one up, or in a test harness that never sets it.
func TestApplyPluginList_NilRegistryDoesNotPanic(t *testing.T) {
	m := &Model{client: &fakeSender{}} // pluginRegistry left nil
	cmd := m.applyPluginList(ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: true}},
	})
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

// Attach is requestPluginList's only call site; nothing else fails if that
// batching gets dropped, so pin it directly rather than relying on
// requestPluginList's own tests to stand in for it. Both attach owners are
// covered — the startup sweep and the post-redial reattach — because each
// builds its own batch.
func TestAttach_RemoteModeAlsoAsksForPluginList(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(m *Model) tea.Cmd
	}{
		{"startup sweep", func(m *Model) tea.Cmd { return m.attachAllDests() }},
		{"post-redial reattach", func(m *Model) tea.Cmd { return m.attachToDest("gpu01") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gpu := newFakeConn()
			m := Model{client: NewRouter(map[string]Client{"gpu01": gpu}), pluginRegistry: plugin.NewRegistry()}
			m.asRemote("gpu01")

			cmd := tc.run(&m)
			if cmd == nil {
				t.Fatal("attach returned no command")
			}
			runCmd(cmd)

			sent := sentTypes(gpu)
			sawAttach, sawPluginList := sent[ipc.MsgAttach], sent[ipc.MsgPluginListReq]
			if !sawAttach {
				t.Error("attach did not send MsgAttach")
			}
			if !sawPluginList {
				t.Error("attach did not also ask for the plugin list in remote mode — every .Available consumer would describe the wrong machine until the next reload")
			}
		})
	}
}

// The ordering here is a correctness requirement, not a style preference:
// handleReloadPlugins ending with its own DetectAvailability is the one
// moment the daemon's registry becomes fresh, so an ask that reaches the
// daemon before the reload lands reads the PRE-reload state.
// reloadPluginsThenAskCmd must issue both sends from inside ONE tea.Cmd
// (never a tea.Batch of two), which this test also pins by calling the
// returned command directly rather than through runCmd — a tea.Batch would
// yield a tea.BatchMsg here instead of nil.
func TestReloadPluginsThenAskCmd_SendsReloadBeforeAskFromOneCommand(t *testing.T) {
	fake := &fakeSender{}
	cmd := reloadPluginsThenAskCmd(fake)
	if cmd == nil {
		t.Fatal("reloadPluginsThenAskCmd returned no command")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("cmd() = %v, want nil (a single command, not a tea.Batch)", msg)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}
	if fake.sent[0].Type != ipc.MsgReloadPlugins {
		t.Errorf("sent[0].Type = %q, want %q — reload must be sent first", fake.sent[0].Type, ipc.MsgReloadPlugins)
	}
	if fake.sent[1].Type != ipc.MsgPluginListReq {
		t.Errorf("sent[1].Type = %q, want %q", fake.sent[1].Type, ipc.MsgPluginListReq)
	}
}
