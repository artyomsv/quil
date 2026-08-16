package config

import "github.com/artyomsv/quil/internal/keymap"

// KeySpecsFromConfig is the ONE place legacy [keybindings] field names
// correspond to registry action IDs.
//
// Exported and living here rather than in internal/tui because two callers need
// it and there must be exactly one copy: the TUI builds its dispatch keymap
// from it, and MigrateBindings diffs it against the shipped defaults. Two
// drifting maps is how a migrated binding goes missing silently.
//
// It covers only the 42 actions that HAVE a [keybindings] field. The twelve
// promoted out of handleKey's reserved-key switch (tab.switch_1..9, tab.next,
// tab.prev, system.shortcuts) never had one; they are bound by
// keymap.DefaultLayer, which sits underneath this as a layer.
func KeySpecsFromConfig(kb KeybindingsConfig) map[keymap.ActionID]string {
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
