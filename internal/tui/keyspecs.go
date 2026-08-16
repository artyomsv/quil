package tui

import (
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/logger"
)

// keySpecsFromConfig is the ONE place legacy KeybindingsConfig field names
// correspond to registry action IDs. Stage 3 replaces this body with a
// bindings.toml read.
//
// STAGE 2 IS A HARD PREREQUISITE FOR THAT, and this comment used to say the
// opposite ("no other TUI code changes"). The notes-mode key split still reads
// m.cfg.Keybindings directly through kbMatches — see handleKey's notesMode
// block and notesKeyExempt — so a Stage 3 that empties the legacy table while
// those readers remain leaves them comparing against empty strings: Alt+E
// stops exiting notes mode, and every structural key stops flushing the editor
// before it fires. Stage 2's job is retiring those three kbMatches call sites;
// only then is this function the last thing standing between config and
// dispatch.
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
		"pane.toggle_hunk":        kb.ToggleHunk,
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

// buildKeymap resolves a config into the dispatch keymap.
//
// Two layers, not one. The registry's own defaults go underneath because the
// legacy [keybindings] table has no field for the twelve actions promoted out
// of handleKey's reserved-key switch (tab.switch_1..9, tab.next, tab.prev,
// system.shortcuts) — building from the config alone would leave Alt+1..9
// bound to nothing at all. The config layer on top still wins for every one of
// the 42 fields it does carry.
//
// Build handles a malformed spec per-action, so there is no whole-config
// fallback to do here.
func buildKeymap(kb config.KeybindingsConfig) (*keymap.Keymap, []keymap.Conflict) {
	km, conflicts := keymap.BuildLayered(keymap.DefaultLayer(), keySpecsFromConfig(kb))
	for _, c := range conflicts {
		logger.Warn("keybindings: %s", c)
	}
	return km, conflicts
}

// isAction reports whether key is bound to the given action, searching both
// tiers. Used by the modal surfaces that sit outside handleKey's tier split
// and still need to recognise one specific action: the paste branches in
// dialog.go, the context menu, and the lazygit overlay. The reconnect screen
// is NOT one of them — it needs the bindings themselves, and reads them with
// Keymap.Keys.
//
// Searching BOTH tiers is deliberate and is why this is not a dispatch
// primitive: these surfaces run before handleKey's tier split, so "is this key
// bound to X" is the only question they can ask. The cost is that a modal can
// recognise an action whose handleKey arm the corresponding tier would never
// reach — harmless for the six call sites, since each asks about one action it
// then handles itself, but a caller using this to decide what handleKey WILL do
// would get the wrong answer for a chord claimed in the other tier.
func (m *Model) isAction(key string, id keymap.ActionID) bool {
	for _, tier := range []keymap.Tier{keymap.TierEarly, keymap.TierLate} {
		if got, ok := m.keymap.MatchTier(tier, key); ok && got == id {
			return true
		}
	}
	return false
}
