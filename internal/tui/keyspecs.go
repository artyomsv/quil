package tui

import (
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/logger"
)

// keySpecsFromConfig is the ONE place legacy KeybindingsConfig field names
// correspond to registry action IDs. Stage 3 replaces this body with a
// bindings.toml read; no other TUI code changes when it does.
func keySpecsFromConfig(kb config.KeybindingsConfig) map[keymap.ActionID]string {
	return map[keymap.ActionID]string{
		"app.quit":                kb.Quit,
		"app.redraw":              kb.Redraw,
		"app.command_palette":     kb.CommandPalette,
		"tab.new":                 kb.NewTab,
		"tab.close":               kb.CloseTab,
		"tab.rename":              kb.RenameTab,
		"tab.cycle_color":         kb.CycleTabColor,
		"pane.close":              kb.ClosePane,
		"pane.restart":            kb.RestartPane,
		"pane.rename":             kb.RenamePane,
		"pane.split_h":            kb.SplitHorizontal,
		"pane.split_v":            kb.SplitVertical,
		"pane.focus_toggle":       kb.FocusPane,
		"pane.notes_toggle":       kb.NotesToggle,
		"pane.paste":              kb.Paste,
		"pane.scroll_page_up":     kb.ScrollPageUp,
		"pane.scroll_page_down":   kb.ScrollPageDown,
		"pane.left":               kb.PaneLeft,
		"pane.right":              kb.PaneRight,
		"pane.up":                 kb.PaneUp,
		"pane.down":               kb.PaneDown,
		"pane.next":               kb.NextPane,
		"pane.prev":               kb.PrevPane,
		"pane.go_back":            kb.GoBack,
		"pane.mute":               kb.MutePane,
		"pane.toggle_eager":       kb.ToggleEager,
		"pane.toggle_wrap":        kb.ToggleWrap,
		"pane.toggle_lazygit":     kb.ToggleLazygit,
		"pane.command_history":    kb.CommandHistory,
		"pane.quick_actions":      kb.QuickActions,
		"notification.toggle":     kb.NotificationToggle,
		"notification.focus":      kb.NotificationFocus,
		"sidebar.toggle":          kb.SidebarToggle,
		"project.new":             kb.NewProject,
		"project.destroy":         kb.DestroyProject,
		"project.picker":          kb.ProjectPicker,
		"project.next":            kb.ProjectNext,
		"project.prev":            kb.ProjectPrev,
		"project.toggle":          kb.ProjectToggle,
		"project.attention_queue": kb.AttentionQueue,
		"json.transform":          kb.JSONTransform,
	}
}

// buildKeymap resolves a config into the dispatch keymap. Build handles a
// malformed spec per-action, so there is no whole-config fallback to do here.
func buildKeymap(kb config.KeybindingsConfig) (*keymap.Keymap, []keymap.Conflict) {
	km, conflicts := keymap.Build(keySpecsFromConfig(kb))
	for _, c := range conflicts {
		logger.Warn("keybindings: %s", c)
	}
	return km, conflicts
}

// isAction reports whether key is bound to the given action, searching both
// tiers. Used by the modal surfaces (dialogs, context menu, lazygit overlay,
// reconnect screen) that sit outside handleKey's tier split.
func (m *Model) isAction(key string, id keymap.ActionID) bool {
	for _, tier := range []keymap.Tier{keymap.TierEarly, keymap.TierLate} {
		if got, ok := m.keymap.MatchTier(tier, key); ok && got == id {
			return true
		}
	}
	return false
}
