package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// Sandbox capability, filed per destination.
//
// The question is about the DAEMON's machine: the container runs where the
// daemon runs, and the client may be a laptop attached to a remote host. One
// shared answer would be the 2026-09-03 bug again, where a single plugin
// registry served every destination and the last host to reply spoke for all
// of them.
//
// It deliberately does NOT copy applyPluginList's two extra rules, and both
// departures matter:
//
//   - That function REFUSES a local answer, because the client re-detects
//     plugin availability itself at every launch and the daemon's frozen
//     answer would be staler. Here the daemon's answer is the ONLY one that
//     exists — the client cannot see the daemon's Docker engine — and the
//     local daemon is the case the whole feature targets.
//   - pluginAvailableFor falls back to LOCAL DETECTION for a destination that
//     has not answered, because a wrong offer fails loudly at spawn while a
//     wrong grey-out hides a working tool silently. There is no local
//     detection to fall back to here, and the failure mode is inverted: a
//     wrong offer means a pane that dies with a docker error. Silence is
//     therefore treated as unavailable.

// sandboxCapMsg carries a daemon's answer back into Update.
type sandboxCapMsg struct {
	Dest string
	Resp ipc.SandboxCapRespPayload
}

// applySandboxCap files one destination's answer.
func (m *Model) applySandboxCap(dest string, resp ipc.SandboxCapRespPayload) {
	if m.destSandbox == nil {
		m.destSandbox = make(map[string]ipc.SandboxCapRespPayload, 1)
	}
	m.destSandbox[dest] = resp
}

// sandboxAvailableFor reports whether a sandbox pane can be started on the
// daemon behind dest.
//
// The single accessor. Every consumer goes through it and names the machine it
// means, so no caller can accidentally ask about "the sandbox" in general.
func (m Model) sandboxAvailableFor(dest string) bool {
	return m.destSandbox[dest].Available
}

// sbxAvailableFor reports whether the sbx backend can run on the daemon
// behind dest. Silence is unavailable, as for docker.
func (m Model) sbxAvailableFor(dest string) bool {
	return m.destSandbox[dest].SbxAvailable
}

// sandboxUnavailableReason is what to tell the user when the row is absent.
// Empty when the destination simply has not answered yet.
func (m Model) sandboxUnavailableReason(dest string) string {
	return sanitizeRemoteText(m.destSandbox[dest].Error)
}

// requestSandboxCap asks one destination whether it can host a sandbox pane.
//
// Sent at attach and again when the create dialog opens: Docker Desktop is
// frequently not running at login and started later, so an answer cached from
// startup is the wrong one for most of a daemon's life. The daemon caches its
// side, so asking twice costs one probe.
func (m *Model) requestSandboxCap(dest string) tea.Cmd {
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgSandboxCapReq, ipc.SandboxCapReqPayload{})
		if err != nil {
			log.Printf("sandbox cap: encode: %v", err)
			return nil
		}
		// sendForDest, never a bare send: attach batches this and the
		// reconnect attach is per-destination, so an unstamped request would
		// be answered by whichever daemon happened to be in the foreground —
		// and the answer filed against the wrong machine. The plugin-list RPC
		// learned that the hard way.
		m.sendForDest(dest, msg)
		return nil
	}
}
