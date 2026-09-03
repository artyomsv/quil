package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// pluginListMsg carries one plugin-availability response and the daemon that
// sent it.
//
// Dest is ipc.Message.Origin, which the router stamps on receive; "" is the
// local daemon, as everywhere else in this client. It is what keeps two hosts'
// answers apart — without it the last reply to arrive spoke for every machine.
//
// No generation field, deliberately, and this is the one RPC in the phase where
// that is right rather than forgotten: responses are now keyed by the daemon
// that sent them and the apply is idempotent, so a late answer says exactly
// what a fresh one from that same host would.
type pluginListMsg struct {
	Resp ipc.PluginListRespPayload
	Dest string
}

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
func (m *Model) requestPluginList() tea.Cmd { return m.requestPluginListFor(m.activeDest()) }

// requestPluginListFor asks ONE named daemon, and every caller that knows which
// daemon it means must use it.
//
// The unstamped version was wrong on the path that matters most: attach batches
// this, and the reconnect attach is per-destination, so a BACKGROUND daemon
// coming back asked whichever daemon happened to be in the FOREGROUND for its
// plugin list — then adopted that answer as the reconnected host's, greying out
// tools that exist there and offering tools that do not. Stamping makes the
// question and the answer describe the same machine.
func (m *Model) requestPluginListFor(dest string) tea.Cmd {
	if !m.remoteModeFor(dest) {
		return nil
	}
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgPluginListReq, ipc.PluginListReqPayload{})
		if err != nil {
			log.Printf("plugin list: encode: %v", err)
			return nil
		}
		m.sendForDest(dest, msg)
		return nil
	}
}

// applyPluginList files ONE daemon's availability answer under ITS OWN
// destination.
//
// Filed rather than adopted, and that distinction is the whole fix. The answer
// describes the machine that sent it and nothing else; writing it into the
// shared registry made the last host to reply speak for every host, so a remote
// with no `claude` greyed out Claude Code in the local project too. `dest` is
// the answering daemon (ipc.Message.Origin, stamped by the router on receive),
// with "" meaning the local one exactly as it does everywhere else in this
// client.
//
// An EMPTY answer is refused: it is not a statement that nothing is installed,
// it is a daemon that told us nothing. Filing it would grey out every plugin on
// that host at once — the silent failure this RPC exists to remove.
//
// A LOCAL answer is refused too, and that guard lives HERE rather than at the
// asker. requestPluginListFor already declines to ask locally, but
// reloadPluginsThenAskCmd asks unconditionally — so a local answer really does
// arrive, on a path the startup migration dialog reaches without the user
// opening anything. Filing it would make the local daemon authoritative for the
// local project: this client re-detects at every launch and at all three reload
// sites, while the daemon detects once at start and then runs for weeks with a
// PATH frozen at spawn. It would also silently undo the DetectAvailability pass
// those reload sites had just run, milliseconds earlier, about the very same
// machine. Refusing at the file keeps the rule true for any future caller
// instead of for one of two.
func (m *Model) applyPluginList(dest string, resp ipc.PluginListRespPayload) tea.Cmd {
	if len(resp.Plugins) == 0 || !m.remoteModeFor(dest) {
		return nil
	}
	avail := make(map[string]bool, len(resp.Plugins))
	for _, p := range resp.Plugins {
		avail[p.Name] = p.Available
	}
	if m.destAvail == nil {
		m.destAvail = make(map[string]map[string]bool, 1)
	}
	m.destAvail[dest] = avail
	return nil
}

// pluginAvailableFor reports whether the plugin named `name` can actually be
// run by the daemon at `dest`. Every .Available consumer in the client goes
// through it, because "is this installed" has no answer that is not about a
// particular machine.
//
// A destination that has answered is authoritative for itself — including a
// plugin ABSENT from its answer, which reads false: the daemon has no
// definition for it, so spawning that type there falls back to "terminal" and
// the pane opens as a shell wearing the wrong name. Greying it is the honest
// answer.
//
// A destination that has NOT answered falls back to the local registry's own
// detection. That covers the local daemon (which never files an answer at all,
// see applyPluginList) and a remote whose answer is still in flight or whose
// daemon is too old to have the RPC — there it keeps the pre-RD-023 behaviour of
// offering what this machine has, because a wrong offer fails loudly at spawn
// while a wrong grey-out hides a working tool silently.
//
// Pointer receiver: Model is large and this runs twice per comparison inside
// sortPluginsAvailableFirst's comparator and once per row in two render loops,
// so a value receiver copies the whole struct on a hot path for nothing.
func (m *Model) pluginAvailableFor(dest, name string) bool {
	if avail, ok := m.destAvail[dest]; ok {
		return avail[name]
	}
	if m.pluginRegistry == nil {
		return false
	}
	p := m.pluginRegistry.Get(name)
	return p != nil && p.Available
}

// reloadPluginsThenAskCmd sends MsgReloadPlugins and MsgPluginListReq from a
// SINGLE tea.Cmd, reload first.
//
// This must never be split into tea.Batch(reloadCmd, askCmd): batched
// commands run on separate goroutines with no ordering guarantee between
// them, so the ask could reach the daemon before the reload does and read
// the daemon's PRE-reload registry — handleReloadPlugins ending with its own
// DetectAvailability is the one moment the daemon's cached answer becomes
// fresh, and that is exactly the moment a raced ask would miss.
// internal/ipc/server.go dispatches one connection's messages sequentially
// on a single goroutine, so two sends issued in order from inside ONE
// command are handled in that same order; two sends from two independently
// scheduled commands are not.
//
// Used by both places the Plugins dialog reloads: the Reload/Restore
// buttons and a TOML editor save. Unconditional (no RemoteMode guard around
// the ask): the daemon's post-reload registry is exactly as fresh as
// whatever the TUI would compute locally, so asking after a reload is
// harmless in local mode too, and keeping the two sites' commands identical
// beats special-casing one of them.
func reloadPluginsThenAskCmd(client tuiClient) tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgReloadPlugins, nil)
		client.Send(msg)
		req, err := ipc.NewMessage(ipc.MsgPluginListReq, ipc.PluginListReqPayload{})
		if err != nil {
			log.Printf("plugin list: encode: %v", err)
			return nil
		}
		client.Send(req)
		return nil
	}
}
