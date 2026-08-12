package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// handleToggleLazygit implements the Alt+G state machine (spec §4).
//
// Precedence:
//  1. Overlay visible → hide it. Never replaces from the overlay itself.
//  2. Overlay hidden/absent → ask the daemon which repos are near
//     activeNormalPane.CWD (requestGitRepos); resumes at step 3 in
//     resolveLazygitOverlay once the answer lands.
//  3. No candidates: if an overlay exists → show it anyway;
//     else flash "no git repo here".
//  4. Any candidate == existing overlay's repo (compare to overlayPane.CWD) →
//     show existing (no binary check; process already running).
//  5. Lazygit availability gate — hoisted above the picker so a missing binary
//     never opens the picker (which would spawn a doomed pane on Enter).
//     Steps 1-4 stay unguarded: showing a running overlay never needs the binary.
//  6. Multiple candidates, none matching → open picker dialog
//     (dialogGitRepoPick; Task 12 fills render/handler).
//  7. Create (or destroy old + create) for the resolved repo.
//     createOverlay keeps its own gate as defense-in-depth for callers that
//     bypass this function (e.g. handleGitRepoPickKey).
func (m *Model) handleToggleLazygit() tea.Cmd {
	tab := m.activeTabModel()
	if tab == nil {
		return nil
	}

	// Step 1: visible overlay → hide.
	if tab.overlayVisible {
		tab.overlayVisible = false
		return tea.Batch(tea.ClearScreen, m.overlayVisibilityCmd(tab, false))
	}

	// Step 2: resolve candidates from the active NORMAL pane's CWD.
	// ActivePaneModel returns the overlay when visible, but we already
	// handled that case above. Use treeActivePaneModel for the normal pane.
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
		return m.resolveLazygitOverlay(tab, nil)
	}
	// Asked of the DAEMON, never resolved here. Running gitdiscover in this
	// process stats the machine drawing the UI, so against a remote host Alt+G
	// reported "no git repo here" for a directory that is a repository on the
	// machine that actually holds it — and nothing in that message hinted the
	// wrong disk had been consulted. The rest of the state machine resumes in
	// applyGitRepos when the answer lands.
	return m.requestGitRepos(cwd, tab.ID, repoScanOverlay)
}

// resolveLazygitOverlay runs steps 3-7 of the Alt+G state machine against an
// already-resolved candidate list.
//
// Split from the discovery above so the decision half can be driven directly:
// handleToggleLazygit calls it for the no-CWD case (candidates known to be
// nil without a round trip), and applyGitRepos calls it once the daemon's
// answer lands (RD-021 — discovery itself runs on the daemon, never stats
// this process's disk). Everything below here is unaffected by where the
// list came from. overlay_test.go's toggleWithDiscovery also drives this half
// directly, resolving candidates the same way the daemon does, so tests don't
// need a live RPC round trip to exercise steps 3-7.
func (m *Model) resolveLazygitOverlay(tab *TabModel, candidates []string) tea.Cmd {
	// Step 3: no candidates.
	if len(candidates) == 0 {
		if tab.overlayPane != nil {
			return m.showOverlay(tab)
		}
		m.setFlash("no git repo here")
		return m.flashCmd()
	}

	// Step 4: check whether any candidate matches the existing overlay's repo.
	if tab.overlayPane != nil {
		for _, c := range candidates {
			if c == tab.overlayPane.CWD {
				return m.showOverlay(tab)
			}
		}
	}

	// Step 5: availability gate — must come before the picker so a missing
	// binary never opens the picker dialog (Enter would spawn a doomed pane).
	p := m.pluginRegistry.Get("lazygit")
	if p == nil || !p.Available {
		m.setFlash("lazygit not installed")
		return m.flashCmd()
	}

	// Step 6: multiple candidates, none matching → picker.
	// Picker has no scroll machinery; same cap as the setup-dialog list.
	if len(candidates) > 1 {
		if len(candidates) > maxRepoCandidates {
			candidates = candidates[:maxRepoCandidates]
		}
		m.repoPickCandidates = candidates
		m.dialog = dialogGitRepoPick
		m.dialogCursor = 0
		return nil
	}

	// Step 7: single candidate — create/replace.
	return m.createOverlay(tab, candidates[0])
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
// design (handleOverlayKey's alt+1..9 arm), while only the active tab of a
// project paints — so for every background tab the flag overstates visibility,
// and an overstated one is exempt from the idle sweep forever.
//
// Scoped to the tab's own project, NOT to the active project, and the asymmetry
// is deliberate: switching projects moves no tab's activeTab, so this answer
// cannot go stale behind a project switch. A stricter rule would report a
// background project's overlay hidden with nothing to re-report true when the
// user switches back — and the sweep would then destroy an overlay they are
// looking at, which is the one wrong-destroy this feature must not have.
func (m *Model) overlayOnScreen(tab *TabModel) bool {
	if tab == nil || !tab.overlayVisible {
		return false
	}
	for _, p := range m.projects {
		for i, t := range p.tabs {
			if t == tab {
				return i == p.activeTab
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

// overlayTruthAllCmd reports the current truth for every tab that owns an
// overlay pane — one fresh message per tab, each aimed at that tab's OWN
// destination, since overlayVisibilityCmd builds its message inside the send.
func (m *Model) overlayTruthAllCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, tab := range m.allTabs() {
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
				log.Printf("overlay policy: encode: %v", err)
				return nil
			}
			if err := m.sendForDest(dest, msg); err != nil {
				log.Printf("overlay policy: send to %q: %v", dest, err)
			}
		}
		return nil
	}
}

// createOverlay destroys any existing overlay pane, initialises
// pendingOverlayShow, and sends MsgCreatePane to the daemon.
//
// Defense-in-depth availability check: handleToggleLazygit already gates on
// this before reaching the picker or this function, but handleGitRepoPickKey
// calls createOverlay directly, so we re-check here to cover any future caller.
func (m *Model) createOverlay(tab *TabModel, repo string) tea.Cmd {
	// Defense-in-depth: re-check availability so any direct caller is safe.
	p := m.pluginRegistry.Get("lazygit")
	if p == nil || !p.Available {
		m.setFlash("lazygit not installed")
		return m.flashCmd()
	}

	var cmds []tea.Cmd

	// The overlay is the tab's pane, so both sends below follow the TAB's
	// daemon — Alt+G is reachable from a background project's tab.
	tabDest := tab.Dest

	// Destroy the old overlay if one exists (different repo).
	if tab.overlayPane != nil {
		oldID := tab.overlayPane.ID
		// Captured before the slot is cleared: overlayVisibilityCmd reads
		// tab.overlayPane, which is nil by the time this batches below.
		visCmd := m.overlayVisibilityCmd(tab, false)
		tab.overlayPane.Dispose()
		tab.overlayPane = nil
		tab.overlayVisible = false
		cmds = append(cmds, visCmd, func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: oldID})
			if err != nil {
				log.Printf("overlay: destroy pane encode: %v", err)
				return nil
			}
			m.sendForDest(tabDest, msg)
			return nil
		})
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
	cmds = append(cmds, func() tea.Msg {
		payload := ipc.CreatePanePayload{
			TabID:        tabID,
			CWD:          repo,
			Type:         "lazygit",
			InstanceArgs: []string{"--path", repo},
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
//   - ToggleLazygit (alt+g)  → hide overlay (delegates to handleToggleLazygit)
//   - Quit (ctrl+q / ctrl+c) → pass through to normal quit handler
//   - Redraw (alt+shift+l)   → mirror the existing Redraw case exactly
//   - alt+1..9               → switchTab (overlay survives, per-tab state)
//   - everything else         → keyToBytes → forwardInputBytes
//     (ActivePaneModel returns the overlay pane when visible, so the bytes
//     reach the lazygit PTY).
//
// Esc MUST reach lazygit (not intercepted here), so it falls through to the
// default forwarding branch.
func (m *Model) handleOverlayKey(msg tea.KeyPressMsg, tab *TabModel) tea.Cmd {
	key := msg.String()
	kb := m.cfg.Keybindings

	logger.Debug("handleOverlayKey: key=%q", key)

	// Toggle → hide.
	if kbMatches(key, kb.ToggleLazygit) {
		return m.handleToggleLazygit()
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
