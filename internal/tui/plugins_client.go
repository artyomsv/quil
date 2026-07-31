package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// pluginListMsg carries one plugin-availability response.
//
// No generation field, deliberately, and this is the one RPC in the phase where
// that is right rather than forgotten: every response describes the same daemon
// and the apply is idempotent, so a late answer says exactly what a fresh one
// would.
type pluginListMsg struct{ Resp ipc.PluginListRespPayload }

// requestPluginList asks the daemon which plugins its own registry can run.
//
// Availability was detected on the machine drawing the UI, which is the wrong
// machine whenever the daemon is remote: a tool installed only on the server was
// greyed out, and one installed only locally was offered and then spawned as a
// fallback terminal.
//
// Remote mode ONLY, which is deliberate and is the one place in this phase that
// does not also exercise itself locally. The daemon's answer is stale by design
// — it detects at start and on plugin reload, and runs for weeks — while the
// TUI re-detects at every launch. Locally that makes the daemon's answer
// strictly worse than the one we already have about the very same machine; a
// tool installed today would stay greyed out until the daemon restarted.
// Remotely the trade inverts: stale about the right machine beats fresh about
// the wrong one.
//
// No timeout tick: there is nothing to time out INTO. An unanswered request
// simply leaves local detection in place, which is the intended failure mode —
// a wrong offer fails loudly at spawn, a wrong grey-out hides a working tool
// silently.
func (m *Model) requestPluginList() tea.Cmd {
	if !m.RemoteMode() {
		return nil
	}
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgPluginListReq, ipc.PluginListReqPayload{})
		if err != nil {
			log.Printf("plugin list: encode: %v", err)
			return nil
		}
		m.client.Send(msg)
		return nil
	}
}

// applyPluginList adopts the daemon's availability answer.
//
// An EMPTY answer is refused: it is not a statement that nothing is installed,
// it is a daemon that told us nothing. Adopting it would grey out every plugin
// at once — the silent failure this change exists to remove.
func (m *Model) applyPluginList(resp ipc.PluginListRespPayload) tea.Cmd {
	if len(resp.Plugins) == 0 || m.pluginRegistry == nil {
		return nil
	}
	avail := make(map[string]bool, len(resp.Plugins))
	for _, p := range resp.Plugins {
		avail[p.Name] = p.Available
	}
	m.pluginRegistry.SetAvailability(avail)
	return nil
}
