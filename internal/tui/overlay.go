package tui

import (
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/gitdiscover"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// handleToggleLazygit implements the Alt+G state machine (spec §4).
//
// Precedence:
//  1. Overlay visible → hide it. Never replaces from the overlay itself.
//  2. Overlay hidden/absent → resolve gitdiscover.Candidates(activeNormalPane.CWD).
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
		return tea.ClearScreen
	}

	// Step 2: resolve candidates from the active NORMAL pane's CWD.
	// ActivePaneModel returns the overlay when visible, but we already
	// handled that case above. Use treeActivePaneModel for the normal pane.
	normalPane := tab.treeActivePaneModel()
	var cwd string
	if normalPane != nil {
		cwd = normalPane.CWD
	}
	candidates := gitdiscover.Candidates(cwd)

	// Step 3: no candidates.
	if len(candidates) == 0 {
		if tab.overlayPane != nil {
			return m.showOverlay(tab)
		}
		m.setFlash("no git repo here")
		return nil
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
		return nil
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
	tab.Resize(tab.Width, tab.Height) // re-sync overlay pane dims
	return tea.Batch(tea.ClearScreen, m.overlayResizeCmd(tab))
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
		return nil
	}

	var cmds []tea.Cmd

	// Destroy the old overlay if one exists (different repo).
	if tab.overlayPane != nil {
		oldID := tab.overlayPane.ID
		tab.overlayPane.Dispose()
		tab.overlayPane = nil
		tab.overlayVisible = false
		cmds = append(cmds, func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: oldID})
			if err != nil {
				log.Printf("overlay: destroy pane encode: %v", err)
				return nil
			}
			m.client.Send(msg)
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
		m.client.Send(msg)
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
	paneID := tab.overlayPane.ID
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
		m.client.Send(msg)
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
		for _, t := range m.tabs {
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
