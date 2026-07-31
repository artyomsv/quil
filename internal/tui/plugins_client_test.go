package tui

import (
	"testing"

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
		if sent := m.client.(*fakeSender).sent; len(sent) != 0 {
			t.Errorf("sent %d messages in local mode, want 0", len(sent))
		}
	}
}

func TestRequestPluginList_RemoteModeAsks(t *testing.T) {
	m := pluginClientModel(t)
	m.SetRemoteDest("gpu01")
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
