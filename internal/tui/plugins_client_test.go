package tui

import (
	"io"
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

// The marker must read AVAILABLE locally, or the fallback returns the same
// answer the assertion is looking for and no implementation can be
// distinguished from any other. "terminal" satisfies that with no detection
// pass at all — builtinTerminal() carries `Available: true` as a struct literal
// — and DetectAvailability is called anyway so the precondition is stated here
// rather than inherited from a literal in another package. Gutting
// applyPluginList to a no-op fails this test.
func TestApplyPluginList_FilesTheServersAnswer(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()
	if !m.pluginAvailableFor("gpu01", "terminal") {
		t.Fatal("precondition: the marker must read available before the answer is filed, or this test cannot fail")
	}

	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})

	if m.pluginAvailableFor("gpu01", "terminal") {
		t.Error("Available = true, want false — the server's answer was ignored")
	}
}

// The LOCAL daemon's answer is refused. reloadPluginsThenAskCmd asks
// unconditionally, so this arrives on a path the startup migration dialog
// reaches without the user opening anything — and filing it would make the
// daemon (detects once at start, then runs for weeks) authoritative over this
// client's own detection about the very same machine, silently undoing the
// DetectAvailability pass the reload had just run.
func TestApplyPluginList_LocalAnswerIsRefused(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability() // local truth: terminal is available

	m.applyPluginList("", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})

	if !m.pluginAvailableFor("", "terminal") {
		t.Error("local availability = false — the local daemon's answer was filed and shadowed local detection")
	}
	if _, filed := m.destAvail[""]; filed {
		t.Error(`destAvail[""] exists — the local daemon must never get a bucket at all`)
	}
}

// An empty answer is not a statement that nothing is installed; it is a daemon
// that answered nothing useful. Keeping the local values fails toward the loud
// error (a wrong offer fails at spawn) instead of the silent one (a wrong
// grey-out hides a working tool).
func TestApplyPluginList_EmptyAnswerKeepsLocalDetection(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()
	m.applyPluginList("gpu01", ipc.PluginListRespPayload{})
	if !m.pluginAvailableFor("gpu01", "terminal") {
		t.Error("Available = false — an empty answer overwrote local detection")
	}
}

// --- availability is per destination ----------------------------------------

// A daemon's answer describes ITS OWN machine, and the client used to keep one
// shared registry for every destination — so the LAST answer to arrive spoke
// for all of them.
//
// Shipped repro (2026-09-03): adding a remote host with no `claude` installed
// made the LOCAL project's Ctrl+N offer "Claude Code (not installed)" while
// claude ran perfectly on the laptop, and nothing ever put it back —
// DetectAvailability only ever sets availability TRUE.
//
// "terminal" is the fixture because DetectAvailability marks it available
// unconditionally, so no binary on the test runner takes part in the answer.
func TestApplyPluginList_RemoteAnswerDoesNotDescribeTheLocalMachine(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()

	m.applyPluginList("pi-hole", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})

	if !m.pluginAvailableFor("", "terminal") {
		t.Error("local availability = false — a remote daemon's answer overwrote the local machine's own detection")
	}
	if m.pluginAvailableFor("pi-hole", "terminal") {
		t.Error("remote availability = true — the answering daemon's own answer was dropped")
	}
}

// Two remotes must not overwrite each other either. With one shared answer the
// winner was whichever host replied last, which is a race between two hosts
// that have nothing to do with one another.
func TestApplyPluginList_EachRemoteKeepsItsOwnAnswer(t *testing.T) {
	m := pluginClientModel(t)

	m.applyPluginList("has-it", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: true}},
	})
	m.applyPluginList("lacks-it", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})

	if !m.pluginAvailableFor("has-it", "terminal") {
		t.Error(`"has-it" reads unavailable — the second host's answer overwrote the first`)
	}
	if m.pluginAvailableFor("lacks-it", "terminal") {
		t.Error(`"lacks-it" reads available`)
	}
}

// A plugin the answering daemon does not define cannot spawn there: that
// daemon falls back to "terminal", so the pane would open as a shell wearing
// the wrong name. Absent must read unavailable FOR THAT HOST — and must not
// bleed into any other, which is why the local answer is asserted alongside it.
func TestPluginAvailableFor_AbsentFromAnAnswerIsUnavailableOnThatHostOnly(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()

	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "something-else", Available: true}},
	})

	if m.pluginAvailableFor("gpu01", "terminal") {
		t.Error(`"gpu01" reads available for a plugin absent from its own answer`)
	}
	if !m.pluginAvailableFor("", "terminal") {
		t.Error("local reads unavailable — another host's answer decided it")
	}
}

// A destination nobody has answered for keeps local detection. That covers a
// remote whose reply is still in flight and one whose daemon is too old to have
// the RPC: a wrong offer fails loudly at spawn, a wrong grey-out hides a
// working tool silently.
//
// BOTH answers are asserted, and one alone would be worthless. Every marker in
// a fresh registry reads available (builtinTerminal's `Available: true` literal,
// copied by terminal-wide), so an "available" assertion on its own cannot tell
// "read the registry" from "return true" — the shape that made four tests in
// this package unfalsifiable at once. Seeding the second marker false is what
// makes the pair pin the fallback to the registry VALUE rather than a constant.
func TestPluginAvailableFor_UnansweredDestinationKeepsLocalDetection(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()
	m.pluginRegistry.Get("terminal-wide").Available = false

	if !m.pluginAvailableFor("never-answered", "terminal") {
		t.Error("terminal reads unavailable at an unanswered host; local detection should stand in")
	}
	if m.pluginAvailableFor("never-answered", "terminal-wide") {
		t.Error("terminal-wide reads available at an unanswered host — the fallback is answering a constant, not the registry")
	}
}

// Forgetting a host forgets what it said. disconnectDest deletes every other
// per-destination table and says so; this one is the first whose leftover entry
// is WRONG rather than merely unreachable, because pluginAvailableFor is read
// for a non-active dest and a present bucket wins over local detection. So a
// host re-added after gaining a tool would grey it out until a fresh answer
// landed — the silent failure this whole change removes.
func TestDisconnectDest_DropsThatHostsPluginAnswer(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability() // local truth: terminal is available
	m.client = NewRouter(map[string]Client{"gpu01": newFakeConn()})
	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})
	if m.pluginAvailableFor("gpu01", "terminal") {
		t.Fatal("setup failed: gpu01's answer was never filed")
	}

	m.disconnectDest("gpu01")

	if !m.pluginAvailableFor("gpu01", "terminal") {
		t.Error("gpu01 still reads unavailable after being forgotten — its stale answer outlived the disconnect")
	}
}

// Driven through Update rather than by calling applyPluginList directly: the
// destination has to survive the message as well as the function, and a
// decision function that is correct in isolation says nothing about the call
// site that feeds it.
func TestUpdate_PluginListIsFiledUnderTheDaemonThatAnswered(t *testing.T) {
	m := pluginClientModel(t)
	m.pluginRegistry.DetectAvailability()

	out, _ := m.Update(pluginListMsg{
		Dest: "pi-hole",
		Resp: ipc.PluginListRespPayload{
			Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
		},
	})
	got := out.(Model)

	if !got.pluginAvailableFor("", "terminal") {
		t.Error("local availability = false after a remote answer reached Update")
	}
	if got.pluginAvailableFor("pi-hole", "terminal") {
		t.Error("remote availability = true — Update dropped the answering destination")
	}
}

// The listen loop is where a response learns which daemon sent it: the router
// stamps ipc.Message.Origin on receive. Losing it here files every remote
// answer under "" — the local daemon — which is the original bug wearing a
// different hat, and no test above would notice.
func TestListenForMessages_PluginListCarriesItsOrigin(t *testing.T) {
	resp, err := ipc.NewMessage(ipc.MsgPluginListResp, ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "terminal", Available: false}},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	resp.Origin = "pi-hole" // what Router.pump stamps on receive

	m := Model{client: &oneShotClient{msg: resp}, pluginRegistry: plugin.NewRegistry()}
	m.asRemote("pi-hole")

	msg := m.listenForMessages()()
	got, ok := msg.(pluginListMsg)
	if !ok {
		t.Fatalf("msg is %T, want pluginListMsg", msg)
	}
	if got.Dest != "pi-hole" {
		t.Errorf("Dest = %q, want %q — the answering daemon was not carried through", got.Dest, "pi-hole")
	}
}

// oneShotClient answers once, then reports EOF.
type oneShotClient struct {
	msg  *ipc.Message
	done bool
}

func (c *oneShotClient) Send(*ipc.Message) error { return nil }

func (c *oneShotClient) Receive() (*ipc.Message, error) {
	if c.done {
		return nil, io.EOF
	}
	c.done = true
	return c.msg, nil
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
	cmd := m.applyPluginList("gpu01", ipc.PluginListRespPayload{
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

// --- which machine each gate asks -------------------------------------------

// The overlay gates ask the TAB's daemon, not the active project's, and nothing
// pinned that until this test.
//
// resolveOverlay is reachable from applyGitRepos with a tab resolved by ID,
// which can belong to a BACKGROUND project — the case overlay.go's comment
// calls out as the reason it takes tab.Dest. Every pre-existing gate test seeds
// availability in the registry alone, so it answers identically whichever
// destination is asked and all of them survive retargeting the argument.
//
// Here the two answers DISAGREE: lazygit is installed on this machine (the
// registry, which is also what the active dest "" falls back to) and absent on
// the tab's host. Asking the wrong one opens the picker on a doomed pane.
func TestResolveOverlay_AsksTheTabsDaemonNotTheActiveOne(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	tab.Dest = "gpu01" // a background project's host; the active project is local
	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "lazygit", Available: false}},
	})
	if !m.pluginAvailableFor("", overlayPluginLazygit) {
		t.Fatal("precondition: lazygit must read available locally, or both dests answer alike")
	}

	runCmd(toggleWithDiscovery(m, tab, repo))

	if m.flashText == "" {
		t.Error("no refusal — the gate asked the active dest, where lazygit IS installed, instead of the tab's host")
	}
	if len(fake.sent) != 0 {
		t.Errorf("sent %d messages — a pane was created for a host that cannot run it", len(fake.sent))
	}
}

// createOverlay's defence-in-depth check owes the same answer as the gate above
// it. handleGitRepoPickKey calls it directly, so a divergence here is reachable
// by picking a repo from the picker rather than by the toggle.
func TestCreateOverlay_DefenceInDepthAsksTheTabsDaemon(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	tab.Dest = "gpu01"
	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "lazygit", Available: false}},
	})

	runCmd(m.createOverlay(tab, repo, overlayPluginLazygit))

	if m.flashText == "" {
		t.Error("no refusal — createOverlay asked the active dest instead of the tab's host")
	}
	if len(fake.sent) != 0 {
		t.Errorf("sent %d messages — the overlay was created on a host that cannot run it", len(fake.sent))
	}
}

// The palette and context menu gate on the ACTIVE project's daemon, which is
// the tab their actions reach. Pinned for the same reason: every other test of
// these two rows seeds the registry, so they cannot tell which dest was asked.
func TestPaletteAndCtxMenu_GateOnTheActiveDaemonsAnswer(t *testing.T) {
	repo := gitRepoDir(t)
	m, _, tab := overlayTestModel(t, repo)
	tab.Dest = "gpu01"
	m.cur().Dest = "gpu01" // this project, and its tab, live on gpu01
	m.applyPluginList("gpu01", ipc.PluginListRespPayload{
		Plugins: []ipc.PluginInfo{{Name: "lazygit", Available: false}},
	})
	if !m.pluginAvailableFor("", overlayPluginLazygit) {
		t.Fatal("precondition: lazygit must read available locally, or both dests answer alike")
	}

	for _, cmd := range m.buildPaletteCommands() {
		if cmd.action == palActLazygit && cmd.enabled {
			t.Error("palette offers lazygit — it asked the local registry, not the active host")
		}
	}
	pane := tab.ActivePaneModel()
	for _, item := range m.buildCtxMenuItems(pane) {
		if item.id == ctxActLazygit && item.enabled {
			t.Error("context menu offers lazygit — it asked the local registry, not the active host")
		}
	}
}
