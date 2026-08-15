// keyreaders_test.go covers Task 8: the modal surfaces outside handleKey
// (Settings/instance-form paste, the context menu, the lazygit overlay, the
// command palette, and the reconnect freeze) that used to read
// m.cfg.Keybindings directly and now dispatch through Model.isAction /
// Model.keymap instead.
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

// TestDialogPaste_HonoursMultiBinding pins isAction itself against the
// multi-binding syntax the raw == compare could never match. isAction has no
// production call site of its own to exercise directly — see the
// handleSettingsKey / handleInstanceFormKey tests below for the actual call
// sites the bug lived in.
func TestDialogPaste_HonoursMultiBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.Paste = "ctrl+v,f8"
	m := Model{cfg: cfg}
	m.initKeymap()
	for _, key := range []string{"ctrl+v", "f8"} {
		t.Run(key, func(t *testing.T) {
			if !m.isAction(key, "pane.paste") {
				t.Errorf("isAction(%q, pane.paste) = false — the raw == compare is still there", key)
			}
		})
	}
	if m.isAction("ctrl+x", "pane.paste") {
		t.Error("isAction matched an unrelated key")
	}
}

func TestKeymapKeys_ForReconnectScreen(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.Quit = "ctrl+q,super+q"
	m := Model{cfg: cfg}
	m.initKeymap()
	keys := m.keymap.Keys("app.quit")
	if len(keys) != 2 {
		t.Fatalf("Keys(app.quit) = %v, want 2 entries", keys)
	}
}

// TestHandleSettingsKey_Paste_HonoursMultiBinding is the actual regression for
// the Settings field editor bug: `case key == m.cfg.Keybindings.Paste` compared
// the WHOLE configured string against one key, so a fallback binding (the "f8"
// half of "ctrl+v,f8") could never win the switch and fell through to the
// default branch, which only appends single-rune keys — "f8" is silently
// dropped. pasteToDialog() is the only non-nil tea.Cmd this branch can return,
// so a non-nil cmd is proof the paste case matched.
func TestHandleSettingsKey_Paste_HonoursMultiBinding(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Keybindings.Paste = "ctrl+v,f8"
	m := Model{cfg: cfg, dialog: dialogSettings, dialogEdit: true}
	m.initKeymap()

	_, cmd := m.handleSettingsKey(tea.KeyPressMsg{Code: tea.KeyF8})
	if cmd == nil {
		t.Fatal("f8 (the fallback half of a multi-binding paste) must trigger paste, not fall through to default")
	}
}

// TestHandleInstanceFormKey_Paste_HonoursMultiBinding is the same regression
// for the instance-creation form's field editor (dialog.go's second `case key
// == m.cfg.Keybindings.Paste` site).
func TestHandleInstanceFormKey_Paste_HonoursMultiBinding(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Keybindings.Paste = "ctrl+v,f8"
	m := Model{
		cfg:            cfg,
		dialog:         dialogInstanceForm,
		dialogEdit:     true,
		selectedPlugin: "terminal",
		pluginRegistry: plugin.NewRegistry(),
	}
	m.initKeymap()

	_, cmd := m.handleInstanceFormKey(tea.KeyPressMsg{Code: tea.KeyF8})
	if cmd == nil {
		t.Fatal("f8 (the fallback half of a multi-binding paste) must trigger paste, not fall through to default")
	}
}

// TestHandleCtxMenuKey_Quit_HonoursMultiBinding: the context menu's Quit case
// used kbMatches (already multi-binding-safe) but read straight from
// m.cfg.Keybindings, which would go stale once Stage 3 moves the binding
// source. This pins the isAction migration by exercising a NON-default quit
// binding through the real handler.
func TestHandleCtxMenuKey_Quit_HonoursMultiBinding(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.cfg.Keybindings.Quit = "ctrl+q,super+q"
	m.initKeymap()
	updated, _ := m.Update(tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)

	_, cmd := got.handleCtxMenuKey("super+q")
	if cmd == nil {
		t.Fatal("a non-primary quit binding must still be honoured by the context menu")
	}
}

// TestHandleOverlayKey_ToggleLazygit_HonoursMultiBinding is the overlay's
// equivalent: a non-default alternate binding on toggle_lazygit must still
// hide the overlay.
func TestHandleOverlayKey_ToggleLazygit_HonoursMultiBinding(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "")
	m.cfg.Keybindings.ToggleLazygit = "alt+g,ctrl+alt+g"
	m.initKeymap() // rebuild: overlayTestModel built the keymap from the default cfg
	overlay := NewPaneModel("pane-o", 1024)
	// Type is load-bearing since the overlay slot became shared: step 1 of
	// handleToggleOverlay hides only when tab.overlayRuns(plugin) — an
	// untyped overlay falls through to the resolve half and SWAPS instead,
	// which is the correct answer for a different tool and the wrong one here.
	overlay.Type = overlayPluginLazygit
	tab.overlayPane = overlay
	tab.overlayVisible = true

	key := tea.KeyPressMsg{Mod: tea.ModCtrl | tea.ModAlt, Code: 'g'}
	cmd := m.handleOverlayKey(key, tab)
	runCmd(cmd)

	if tab.overlayVisible {
		t.Error("a non-primary toggle_lazygit binding must still hide the overlay")
	}
}

// TestIsFreezeEscape_HonoursMultiBinding pins the signature change from a raw
// spec string (parsed internally, comma-split) to a pre-resolved []string —
// the reconnect screen now passes m.keymap.Keys("app.quit") rather than
// m.cfg.Keybindings.Quit.
func TestIsFreezeEscape_HonoursMultiBinding(t *testing.T) {
	t.Parallel()
	quit := []string{"ctrl+q", "super+q"}
	for _, key := range quit {
		if !isFreezeEscape(key, quit) {
			t.Errorf("isFreezeEscape(%q, %v) = false, want true", key, quit)
		}
	}
	if isFreezeEscape("a", quit) {
		t.Error("isFreezeEscape matched an unrelated key")
	}
	// An unbound quit action yields a nil/empty slice from Keymap.Keys — the
	// hardcoded freezeEscapeKeys fallback must still apply so an empty
	// `quit = ""` config can never leave a frozen session with no way out.
	if !isFreezeEscape("ctrl+c", nil) {
		t.Error("isFreezeEscape must fall back to freezeEscapeKeys when configuredQuit is empty")
	}
}
