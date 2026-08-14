package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// Plugins that can own a tab's overlay slot. Both are full-tab git tools
// resolved from the active pane's repository; they differ only in which binary
// runs and how the repo reaches it.
//
// Adding a third? overlayInstanceArgs below switches on the NAME, so a new tool
// silently inherits the nil-args branch — correct only if it, like hunk, is
// scoped by its working directory rather than by a repo flag. Check there, and
// give it a keybinding that is not a documented PTY passthrough (see the
// PaneLeft comment in internal/config/config.go).
const (
	overlayPluginLazygit = "lazygit"
	overlayPluginHunk    = "hunk"
)

// handleToggleLazygit (Alt+G) and handleToggleHunk (Alt+H) are the two keys
// bound to the shared overlay state machine.
func (m *Model) handleToggleLazygit() tea.Cmd { return m.handleToggleOverlay(overlayPluginLazygit) }
func (m *Model) handleToggleHunk() tea.Cmd    { return m.handleToggleOverlay(overlayPluginHunk) }

// handleToggleOverlay implements the overlay state machine (spec §4) for one
// overlay plugin.
//
// Precedence:
//  1. THIS plugin's overlay is visible → hide it. Never replaces from the
//     overlay itself. Another tool's visible overlay falls through to the
//     resolve half, which replaces it — a tab has ONE overlay slot, so Alt+H
//     over a visible lazygit is a request to swap tools, not to hide one.
//  2. Overlay hidden/absent/another tool's → ask the daemon which repos are
//     near activeNormalPane.CWD (requestGitRepos); resumes at step 3 in
//     resolveOverlay once the answer lands.
//  3. No candidates: if an overlay for THIS plugin exists → show it anyway;
//     else flash "no git repo here".
//  4. Any candidate == existing overlay's repo AND that overlay runs this
//     plugin → show existing (no binary check; process already running).
//  5. Availability gate — hoisted above the picker so a missing binary
//     never opens the picker (which would spawn a doomed pane on Enter).
//     Steps 1-4 stay unguarded: showing a running overlay never needs the binary.
//  6. Multiple candidates, none matching → open picker dialog
//     (dialogGitRepoPick), which remembers the requesting plugin.
//  7. Create (or destroy old + create) for the resolved repo.
//     createOverlay keeps its own gate as defense-in-depth for callers that
//     bypass this function (e.g. handleGitRepoPickKey).
//
// Every step keys on pluginName rather than on the mere existence of an
// overlay: with one slot shared between tools, "an overlay is up" and "the
// overlay the user just asked for is up" are different questions, and
// answering the first gives the user the other tool.
func (m *Model) handleToggleOverlay(pluginName string) tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}

	// Step 1: this plugin's overlay is visible → hide.
	if tab.overlayVisible && tab.overlayRuns(pluginName) {
		tab.overlayVisible = false
		return tea.Batch(tea.ClearScreen, m.overlayVisibilityCmd(tab, false))
	}

	// Step 2: resolve candidates from the active NORMAL pane's CWD.
	// ActivePaneModel returns the overlay when one is visible — which is
	// reachable here, since another tool's overlay does not take the hide
	// branch above — so the normal pane has to be asked for by name. Taking
	// the overlay's own CWD instead would resolve the swap against the repo
	// the outgoing tool happened to be opened on.
	normalPane := tab.treeActivePaneModel()
	var cwd string
	if normalPane != nil {
		cwd = normalPane.CWD
	}
	// No CWD means the pane has not reported one yet (no OSC 7, or a pane that
	// never had a shell). Asking the daemon would have it substitute its OWN
	// default and answer about a directory the user is not in — so an overlay
	// could open on an unrelated repository. Fall through to the same
	// no-candidates handling the discovery would have produced.
	if cwd == "" {
		return m.resolveOverlay(tab, nil, pluginName)
	}
	// Asked of the DAEMON, never resolved here. Running gitdiscover in this
	// process stats the machine drawing the UI, so against a remote host Alt+G
	// reported "no git repo here" for a directory that is a repository on the
	// machine that actually holds it — and nothing in that message hinted the
	// wrong disk had been consulted. The rest of the state machine resumes in
	// applyGitRepos when the answer lands.
	return m.requestGitRepos(cwd, tab.ID, repoScanOverlay, pluginName)
}

// resolveOverlay runs steps 3-7 of the overlay state machine against an
// already-resolved candidate list, for the plugin whose key was pressed.
//
// Split from the discovery above so the decision half can be driven directly:
// handleToggleOverlay calls it for the no-CWD case (candidates known to be
// nil without a round trip), and applyGitRepos calls it once the daemon's
// answer lands (RD-021 — discovery itself runs on the daemon, never stats
// this process's disk). Everything below here is unaffected by where the
// list came from. The tests' toggleWithDiscovery helpers also drive this half
// directly, resolving candidates the same way the daemon does, so they don't
// need a live RPC round trip to exercise steps 3-7.
func (m *Model) resolveOverlay(tab *TabModel, candidates []string, pluginName string) tea.Cmd {
	// Step 3: no candidates. Only THIS plugin's overlay is worth showing —
	// popping the other tool up in response to a key that asks for this one
	// answers a question nobody asked.
	if len(candidates) == 0 {
		if tab.overlayRuns(pluginName) {
			return m.showOverlay(tab)
		}
		m.setFlash("no git repo here")
		return m.flashCmd()
	}

	// Step 4: check whether any candidate matches the existing overlay's repo.
	// The repo alone is not enough: with one slot per tab, an overlay on the
	// right repo running the WRONG tool has to be replaced, not shown.
	if tab.overlayRuns(pluginName) {
		for _, c := range candidates {
			if c == tab.overlayPane.CWD {
				return m.showOverlay(tab)
			}
		}
	}

	// Step 5: availability gate — must come before the picker so a missing
	// binary never opens the picker dialog (Enter would spawn a doomed pane).
	p := m.pluginRegistry.Get(pluginName)
	if p == nil || !p.Available {
		m.setFlash(pluginName + " not installed")
		return m.flashCmd()
	}

	// Step 6: multiple candidates, none matching → picker.
	// Picker has no scroll machinery; same cap as the setup-dialog list.
	if len(candidates) > 1 {
		if len(candidates) > maxRepoCandidates {
			candidates = candidates[:maxRepoCandidates]
		}
		m.repoPickCandidates = candidates
		// Which key opened the picker is not recoverable at Enter time — the
		// dialog outlives the keypress — so it is remembered here.
		m.repoPickPlugin = pluginName
		m.dialog = dialogGitRepoPick
		m.dialogCursor = 0
		return nil
	}

	// Step 7: single candidate — create/replace.
	return m.createOverlay(tab, candidates[0], pluginName)
}

// showOverlay makes the overlay visible and syncs its dimensions to the full
// tab area (it may have been hidden during a resize).
func (m *Model) showOverlay(tab *TabModel) tea.Cmd {
	tab.overlayVisible = true
	// Re-assert the canvas before resizing so a wide-canvas overlay toggled
	// before any View()/resizeTabs pass still sizes correctly (today's
	// overlays are lazygit/non-canvas, so this is robustness, not a live bug).
	tab.SetCanvas(tab.Width, tab.Height)
	tab.Resize(tab.Width, tab.Height) // re-sync overlay pane dims
	return tea.Batch(tea.ClearScreen, m.overlayResizeCmd(tab), m.overlayVisibilityCmd(tab, true))
}

// overlayVisibilityCmd tells the daemon whether this tab's overlay is on
// screen, so its idle timer measures HIDDEN time rather than quiet time.
//
// Sent for the TAB's destination, like every other overlay message: Alt+G is
// reachable from a background project's tab.
func (m *Model) overlayVisibilityCmd(tab *TabModel, visible bool) tea.Cmd {
	if tab == nil || tab.overlayPane == nil {
		return nil
	}
	paneID, dest := tab.overlayPane.ID, tab.Dest
	v := visible
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID:         paneID,
			OverlayVisible: &v,
		})
		if err != nil {
			log.Printf("overlay: visibility encode: %v", err)
			return nil
		}
		if err := m.sendForDest(dest, msg); err != nil {
			log.Printf("overlay: visibility send: %v", err)
		}
		return nil
	}
}

// overlayOnScreen reports whether this tab's overlay is actually being
// rendered.
//
// tab.overlayVisible is not the whole answer: it survives a tab switch by
// design (handleOverlayKey's alt+1..9 arm), while only the active tab of the
// ACTIVE PROJECT paints — so for every background tab the flag overstates
// visibility, and an overstated one is exempt from the idle sweep forever.
//
// Both levels are required, and the project one was added late. Scoping to the
// tab's own project alone was defended on the grounds that a stricter rule
// "would report a background project's overlay hidden with nothing to re-report
// true when the user switches back". That was true before the truth protocol
// existed; switchProject reports the transition now, exactly as switchTab does
// one level down, so the tighter rule is safe — and without it a background
// project's overlay stayed marked visible for the life of the daemon, defeating
// idle eviction for the multi-project case entirely.
func (m *Model) overlayOnScreen(tab *TabModel) bool {
	if tab == nil || !tab.overlayVisible {
		return false
	}
	for pi, p := range m.projects {
		for i, t := range p.tabs {
			if t == tab {
				return i == p.activeTab && pi == m.activeProject
			}
		}
	}
	return false
}

// overlayTruthCmd reports this tab's CURRENT visibility, whatever it is.
//
// The five sites that flip tab.overlayVisible are not the only moments the
// truth changes, and the daemon has no other source for it: a tab switch
// changes which overlay paints without touching any flag, and an attach meets a
// daemon whose copy may be stale in either direction (a TUI that exited with an
// overlay on screen left it marked visible; a transient last-client disconnect
// stamped every overlay hidden). Both paths go through this one helper so they
// cannot drift apart.
func (m *Model) overlayTruthCmd(tab *TabModel) tea.Cmd {
	if tab == nil || tab.overlayPane == nil {
		return nil
	}
	return m.overlayVisibilityCmd(tab, m.overlayOnScreen(tab))
}

// overlayTruthTransitionCmd reports overlay truth for both halves of an
// active-tab change: the tab being LEFT (whose overlay, if any, just went off
// screen) and the tab being ENTERED (whose overlay, if any, just came on
// screen). Neither transition flips tab.overlayVisible, so without this pair
// the daemon's copy is wrong for as long as the user stays away — and a
// "visible" overlay left behind is exempt from the idle sweep forever, while
// an overlay entered while still stamped hidden is one sweep away from being
// destroyed out from under the user.
//
// Shared by every path that changes which tab is on screen — switchTab
// (Alt+1..9), jumpToPane (MCP set_active_pane, the notification sidebar's
// navigate, pane-history back-navigation, the palette's goToPane),
// jumpToNextBlocked (Alt+Shift+A, the palette's blocked-queue jump),
// switchProject (Alt+P, the sidebar's project rows, the last-project bounce)
// and applyWorkspaceState's adoption of a daemon-side active-tab change — so
// the two-report rule has one implementation rather than five copies that can
// drift apart.
//
// from may be nil (no tab was active yet) or equal to target (a same-tab
// call, e.g. goToPane's own switchTab on the tab jumpToPane already moved
// to): both report only the target's current truth, exactly like
// overlayTruthCmd's nil-tab handling.
func (m *Model) overlayTruthTransitionCmd(from, target *TabModel) tea.Cmd {
	var cmds []tea.Cmd
	if from != nil && from != target {
		if cmd := m.overlayTruthCmd(from); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if cmd := m.overlayTruthCmd(target); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 0 {
		return nil
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Batch(cmds...)
}

// overlayTruthDestCmd reports the current truth for every tab that owns an
// overlay pane and belongs to ONE destination — what a reconnect wants,
// because only that daemon lost its client and only its copy of visibility
// can have gone stale. Re-reporting a daemon that never dropped would
// re-stamp its overlays' OverlayShownAt and perturb an LRU order nobody asked
// about — the same reason attachAllDests scopes its own post-attach report to
// just the destinations that attached in that round, rather than every
// destination this client knows about.
func (m *Model) overlayTruthDestCmd(dest string) tea.Cmd {
	return m.overlayTruthCmds(func(t *TabModel) bool { return t.Dest == dest })
}

// overlayTruthCmds reports the current truth for every tab that owns an
// overlay pane and satisfies want — one fresh message per tab, each aimed at
// that tab's OWN destination, since overlayVisibilityCmd builds its message
// inside the send.
func (m *Model) overlayTruthCmds(want func(*TabModel) bool) tea.Cmd {
	var cmds []tea.Cmd
	for _, tab := range m.allTabs() {
		if !want(tab) {
			continue
		}
		if cmd := m.overlayTruthCmd(tab); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// overlayPolicyCmd pushes the client's overlay retention settings (idle
// timeout + live cap) to every daemon.
//
// Needed because every Settings setter only flips m.configChanged, and
// cmd/quil/main.go writes config.toml only on TUI exit — the daemon reads
// that file at startup, so without an explicit push a settings change would
// not reach an already-running daemon until its next start. Sent once after
// attach (attachAllDests) and again on every Settings commit that touches
// these two rows (handleSettingsKey).
//
// Guarded on m.client like attachAllDests: a Model built directly by a test,
// with no connection, must produce no command rather than one that panics
// when invoked.
func (m *Model) overlayPolicyCmd() tea.Cmd {
	if m.client == nil {
		return nil
	}
	p := ipc.OverlayPolicyPayload{
		IdleTimeoutMinutes: m.cfg.Overlay.IdleTimeoutMinutes,
		MaxLive:            m.cfg.Overlay.MaxLive,
	}
	dests := m.knownDests()
	return func() tea.Msg {
		// A fresh Message per destination — sendForDest stamps Origin, so a
		// message shared across destinations would be re-stamped mid-flight.
		for _, dest := range dests {
			msg, err := ipc.NewMessage(ipc.MsgOverlayPolicy, p)
			if err != nil {
				// continue, not return: the loop is per-destination and one
				// failure must not silently leave 2..N on a stale policy. The
				// payload is two ints, so this is unreachable today — the point
				// is that the loop reads as tolerant and was not.
				log.Printf("overlay policy: encode for %q: %v", dest, err)
				continue
			}
			if err := m.sendForDest(dest, msg); err != nil {
				log.Printf("overlay policy: send to %q: %v", dest, err)
			}
		}
		return nil
	}
}

// overlayInstanceArgs returns the per-spawn args an overlay pane needs to be
// scoped to repo.
//
// Lazygit takes the repository as an explicit flag. Hunk has no equivalent —
// it reads whatever repository its working directory sits in — and the empty
// return is load-bearing rather than merely unnecessary: the daemon's
// resolveSpawnArgs REPLACES a plugin's base args with InstanceArgs whenever a
// pane has any, and hunk's `diff` subcommand lives in those base args. Any
// non-nil value here would spawn a help screen.
func overlayInstanceArgs(pluginName, repo string) []string {
	if pluginName == overlayPluginLazygit {
		return []string{"--path", repo}
	}
	return nil
}

// createOverlay destroys any existing overlay pane, initialises
// pendingOverlayShow, and sends MsgCreatePane to the daemon.
//
// Defense-in-depth availability check: handleToggleOverlay already gates on
// this before reaching the picker or this function, but handleGitRepoPickKey
// calls createOverlay directly, so we re-check here to cover any future caller.
func (m *Model) createOverlay(tab *TabModel, repo, pluginName string) tea.Cmd {
	// Defense-in-depth: re-check availability so any direct caller is safe.
	p := m.pluginRegistry.Get(pluginName)
	if p == nil || !p.Available {
		m.setFlash(pluginName + " not installed")
		return m.flashCmd()
	}

	var cmds []tea.Cmd

	// The overlay is the tab's pane, so both sends below follow the TAB's
	// daemon — Alt+G is reachable from a background project's tab.
	tabDest := tab.Dest

	// Destroy the old overlay if one exists (different repo, or a different
	// tool now that the slot is shared). oldID stays empty when there is
	// nothing to replace; the send below reads it to decide.
	var oldID string
	if tab.overlayPane != nil {
		oldID = tab.overlayPane.ID
		// Captured before the slot is cleared: overlayVisibilityCmd reads
		// tab.overlayPane, which is nil by the time this batches below.
		visCmd := m.overlayVisibilityCmd(tab, false)
		// Repaint only when the outgoing overlay was ON SCREEN. That case was
		// unreachable until the slot became shared — step 1 hid a visible
		// overlay before this function could be called — and it takes the frame
		// from a full-tab overlay back to the tab's normal split layout with the
		// replacement still a daemon round trip away. Both other visibility
		// transitions (the hide branch and showOverlay) pair with ClearScreen
		// for the same reason. Replacing a HIDDEN overlay changes nothing on
		// screen, so it stays repaint-free — that is the ordinary Alt+G
		// swap-repos path and does not deserve a full redraw.
		if tab.overlayVisible {
			cmds = append(cmds, tea.ClearScreen)
		}
		tab.overlayPane.Dispose()
		tab.overlayPane = nil
		tab.overlayVisible = false
		cmds = append(cmds, visCmd)
	}

	// Record that we expect the overlay to appear and auto-show on arrival.
	if m.pendingOverlayShow == nil {
		m.pendingOverlayShow = make(map[string]bool)
	}
	// Known race window: a concurrent foreign workspace_state arriving between
	// our local slot-clear above and the daemon processing the destroy can
	// re-adopt the dying pane and consume this show intent (the next broadcast
	// self-heals; a second Alt+G shows the new overlay). Tab-keyed is the
	// pragmatic v1 choice because the daemon mints the new pane ID.
	m.pendingOverlayShow[tab.ID] = true

	tabID := tab.ID
	// The destroy and the create leave in ONE Cmd, destroy first — never as two
	// siblings of a tea.Batch, which runs its children on separate goroutines
	// and orders nothing (the same property that transposed keystrokes when
	// pane input was forwarded per-key through Cmds). The daemon dispatches one
	// connection's messages in arrival order, so sending them here in sequence
	// is what makes the replace atomic from its point of view.
	//
	// Losing that race is not cosmetic: enforceOverlayCap runs at CREATE time
	// and counts every live overlay, so a create that arrives first sees the
	// outgoing overlay still alive, finds itself one past [overlay] max_live,
	// and evicts the least recently SHOWN overlay to make room. The outgoing one
	// was just on screen, so it sorts newest and survives — the overlay that
	// dies is a DIFFERENT tab's, typically a lazygit left running elsewhere,
	// which mutates .git and may be mid-rebase. The shared slot is what makes
	// this routine rather than rare: this path used to run only when the
	// resolved repo differed, and now every tool swap goes through it.
	cmds = append(cmds, func() tea.Msg {
		if oldID != "" {
			msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: oldID})
			if err != nil {
				// Fall through to the create: an overlay we failed to destroy is
				// a leak the idle sweep still reclaims, whereas skipping the
				// create would leave the user's keypress with nothing to show.
				log.Printf("overlay: destroy pane encode: %v", err)
			} else {
				m.sendForDest(tabDest, msg)
			}
		}
		payload := ipc.CreatePanePayload{
			TabID:        tabID,
			CWD:          repo,
			Type:         pluginName,
			InstanceArgs: overlayInstanceArgs(pluginName, repo),
			Overlay:      true,
		}
		msg, err := ipc.NewMessage(ipc.MsgCreatePane, payload)
		if err != nil {
			log.Printf("overlay: create pane encode: %v", err)
			return nil
		}
		m.sendForDest(tabDest, msg)
		return nil
	})

	return tea.Batch(cmds...)
}

// overlayResizeCmd sends MsgResizePane for the overlay pane so the daemon's
// PTY tracks the current tab dimensions. Cols/Rows subtract the 2-cell border;
// each dimension is clamped to at least 1.
func (m *Model) overlayResizeCmd(tab *TabModel) tea.Cmd {
	if tab.overlayPane == nil {
		return nil
	}
	paneID, dest := tab.overlayPane.ID, tab.Dest
	cols := tab.Width - 2
	rows := tab.Height - 2
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	c := uint16(cols)
	r := uint16(rows)
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{
			PaneID: paneID,
			Cols:   c,
			Rows:   r,
		})
		if err != nil {
			log.Printf("overlay: resize pane encode: %v", err)
			return nil
		}
		m.sendForDest(dest, msg)
		return nil
	}
}

// handleOverlayKey routes keys while the overlay is visible.
//
// Allow-list:
//   - ToggleLazygit (alt+g)  → hide, or swap to lazygit if hunk is showing
//   - ToggleHunk (alt+h)     → hide, or swap to hunk if lazygit is showing
//   - Quit (ctrl+q / ctrl+c) → pass through to normal quit handler
//   - Redraw (alt+shift+l)   → mirror the existing Redraw case exactly
//   - alt+1..9               → switchTab (overlay survives, per-tab state)
//   - everything else         → keyToBytes → forwardInputBytes
//     (ActivePaneModel returns the overlay pane when visible, so the bytes
//     reach the overlay tool's PTY).
//
// BOTH toggles are on the list, not just the one that owns the visible
// overlay: with a single slot, the other tool's key is how you swap, and a key
// left off this list is delivered to the running tool as input instead.
//
// Esc MUST reach the overlay tool (not intercepted here), so it falls through
// to the default forwarding branch.
func (m *Model) handleOverlayKey(msg tea.KeyPressMsg, tab *TabModel) tea.Cmd {
	key := msg.String()
	kb := m.cfg.Keybindings

	logger.Debug("handleOverlayKey: key=%q", key)

	// Toggles → hide (own tool) or swap (other tool).
	if kbMatches(key, kb.ToggleLazygit) {
		return m.handleToggleLazygit()
	}
	if kbMatches(key, kb.ToggleHunk) {
		return m.handleToggleHunk()
	}

	// Quit.
	if kbMatches(key, kb.Quit) {
		return tea.Quit
	}

	// Redraw — mirrors the Redraw case in handleKey exactly.
	if kbMatches(key, kb.Redraw) {
		for _, t := range m.allTabs() {
			t.invalidateLeaves()
			if t.Root != nil {
				for _, pane := range t.Leaves() {
					pane.invalidateRenderCache()
				}
			}
		}
		return tea.Batch(tea.ClearScreen, sizePollProbe)
	}

	// alt+1..9 → tab switch (overlay per-tab state survives).
	switch key {
	case "alt+1", "alt+2", "alt+3", "alt+4",
		"alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		idx := int(key[len(key)-1] - '1')
		return m.switchTab(idx)
	}

	// Everything else → forward to the overlay PTY.
	// ActivePaneModel returns the overlay pane while overlayVisible is true,
	// so forwardInputBytes routes bytes to the correct PTY.
	data := keyToBytes(msg)
	if data == nil {
		return nil
	}
	tab.overlayPane.ResetScroll()
	return m.forwardInputBytes(data)
}
