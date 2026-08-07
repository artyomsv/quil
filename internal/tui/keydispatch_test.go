package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
	"github.com/artyomsv/quil/internal/plugin"
)

func TestModel_TierLookupRespectsRawKeySeam(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.initKeymap()
	// alt+m is pane.mute, EARLY: visible before tryPluginRawKey, so a plugin
	// declaring raw_keys = ["alt+m"] cannot steal it.
	if _, ok := m.keymap.MatchTier(keymap.TierEarly, "alt+m"); !ok {
		t.Error("pane.mute is not visible to the early tier")
	}
	// alt+r is pane.restart, LATE: must NOT be visible early, so a plugin
	// declaring raw_keys = ["alt+r"] still wins, as it does today.
	if _, ok := m.keymap.MatchTier(keymap.TierEarly, "alt+r"); ok {
		t.Error("pane.restart leaked into the early tier")
	}
	if _, ok := m.keymap.MatchTier(keymap.TierLate, "alt+r"); !ok {
		t.Error("pane.restart is not visible to the late tier")
	}
}

func TestKbMatches_StillWorksForNotesPath(t *testing.T) {
	if !kbMatches("alt+f2", "alt+f2,alt+shift+r") {
		t.Error("kbMatches lost multi-binding support")
	}
	if kbMatches("alt+x", "alt+f2,alt+shift+r") || kbMatches("", "alt+f2") || kbMatches("alt+f2", "") {
		t.Error("kbMatches matched something it should not")
	}
}

// keyPress builds the tea.KeyPressMsg for an "alt+<letter>" chord. Bubble Tea
// v2's field shape is easy to get wrong — a non-empty Text makes String()
// render the text instead of the chord — so the message is verified to render
// as the chord asked for before it is handed back.
func keyPress(t *testing.T, chord string) tea.KeyPressMsg {
	t.Helper()
	letter, ok := strings.CutPrefix(chord, "alt+")
	if !ok || len(letter) != 1 {
		t.Fatalf("keyPress: %q is not an alt+<letter> chord", chord)
	}
	msg := tea.KeyPressMsg{Code: rune(letter[0]), Mod: tea.ModAlt}
	if got := msg.String(); got != chord {
		t.Fatalf("keyPress(%q) built a message that renders as %q", chord, got)
	}
	return msg
}

// rawKeysPaneType is the plugin name the raw-keys fixture registers. It is not
// a real plugin: no shipped one declares raw_keys, so the seam test has to
// bring its own.
const rawKeysPaneType = "rawkeys-fixture"

// givePaneRawKeys points the pane at a plugin whose RawKeys claim keys, and
// wires the registry holding it onto the model. tryPluginRawKey resolves the
// active pane's Type through Model.pluginRegistry, so both halves are needed.
func givePaneRawKeys(t *testing.T, m *Model, paneID string, keys []string) {
	t.Helper()
	dir := t.TempDir()
	toml := "[plugin]\nname = \"" + rawKeysPaneType + "\"\nschema_version = 7\n" +
		"[command]\ncmd = \"true\"\nraw_keys = [\"" + strings.Join(keys, "\", \"") + "\"]\n"
	if err := os.WriteFile(filepath.Join(dir, rawKeysPaneType+".toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	p := reg.Get(rawKeysPaneType)
	if p == nil {
		t.Fatalf("fixture plugin %q did not load", rawKeysPaneType)
	}
	if len(p.Command.RawKeys) != len(keys) {
		t.Fatalf("fixture RawKeys = %v, want %v", p.Command.RawKeys, keys)
	}
	m.pluginRegistry = reg

	pane, _, _ := m.findPaneAndTab(paneID)
	if pane == nil {
		t.Fatalf("pane %q not found in the fixture model", paneID)
	}
	pane.Type = rawKeysPaneType
}

// TestUpdate_RawKeysLoseToEarlyActionsAndBeatLateOnes is the behavioural guard
// for the tier split. tryPluginRawKey sits between the two lookups, so:
//
//   - pane.mute (alt+m) is TierEarly — the action fires, nothing reaches the PTY
//     even though the plugin claims the key.
//   - pane.restart (alt+r) is TierLate — the plugin claim wins, bytes reach the
//     PTY and no confirm dialog opens.
//
// Collapsing the two switches into one lookup flips exactly one of these,
// whichever side of tryPluginRawKey the single lookup lands on. The control
// rows (no raw_keys) pin that both actions still fire when nothing claims the
// key, so a lookup that simply stopped resolving cannot pass this test either.
func TestUpdate_RawKeysLoseToEarlyActionsAndBeatLateOnes(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		rawKeys    bool
		pluginWins bool
	}{
		{"early action beats raw_keys", "alt+m", true, false},
		{"late action loses to raw_keys", "alt+r", true, true},
		{"early action fires with no raw_keys", "alt+m", false, false},
		{"late action fires with no raw_keys", "alt+r", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := inputOrderTestModel(t, "pane-1", true)
			m.cfg = config.Default()
			m.initKeymap()
			if tt.rawKeys {
				givePaneRawKeys(t, m, "pane-1", []string{"alt+m", "alt+r"})
			}

			updated, _ := m.Update(keyPress(t, tt.key))

			var forwarded bool
			select {
			case <-m.inputCh:
				forwarded = true
			default:
			}
			if forwarded != tt.pluginWins {
				t.Errorf("key %q forwarded to PTY = %v, want %v", tt.key, forwarded, tt.pluginWins)
			}

			// The restart confirm is the visible half of "the late action ran".
			// It must be absent exactly when the plugin took the key.
			out, ok := updated.(Model)
			if !ok {
				t.Fatalf("Update returned %T, want Model", updated)
			}
			wantConfirm := tt.key == "alt+r" && !tt.pluginWins
			gotConfirm := out.dialog == dialogConfirm && out.confirmKind == confirmKindRestartPane
			if gotConfirm != wantConfirm {
				t.Errorf("key %q opened the restart confirm = %v, want %v", tt.key, gotConfirm, wantConfirm)
			}
		})
	}
}

// TestHandleKey_PasteAliasesLoseToLateActions pins the one deliberate
// precedence change in the paste block. Before the registry, ctrl+alt+v and f8
// shared a case arm with kb.Paste, so they beat every action defined AFTER it
// but lost to every action before — including pane.restart. Moving the aliases
// out to their own check after the whole tier lookup keeps the half that a user
// can actually hit: `restart_pane = "f8"` still restarts rather than pasting.
func TestHandleKey_PasteAliasesLoseToLateActions(t *testing.T) {
	m, _ := inputOrderTestModel(t, "pane-1", true)
	cfg := config.Default()
	cfg.Keybindings.RestartPane = "f8"
	m.cfg = cfg
	m.initKeymap()

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF8})

	out, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	if out.dialog != dialogConfirm || out.confirmKind != confirmKindRestartPane {
		t.Errorf("f8 bound to restart_pane opened dialog=%v kind=%v; want the restart confirm — "+
			"the paste alias must not outrank a configured late action", out.dialog, out.confirmKind)
	}
}

// TestHandleKey_PasteAliasesStillPasteWhenUnbound is the other half: with no
// action on them, ctrl+alt+v and f8 must still reach pasteClipboard. They are
// the only paste that works in Windows Terminal, which eats ctrl+v.
func TestHandleKey_PasteAliasesStillPaste(t *testing.T) {
	origText, origImg := clipboardReadText, clipboardReadImage
	t.Cleanup(func() { clipboardReadText, clipboardReadImage = origText, origImg })
	clipboardReadText = func() (string, error) { return "PASTED", nil }
	clipboardReadImage = func() ([]byte, error) { return nil, nil }

	for _, press := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"f8", tea.KeyPressMsg{Code: tea.KeyF8}},
		{"ctrl+alt+v", tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl | tea.ModAlt}},
	} {
		t.Run(press.name, func(t *testing.T) {
			m, _ := inputOrderTestModel(t, "pane-1", true)
			m.cfg = config.Default()
			m.initKeymap()

			_, cmd := m.Update(press.msg)
			if cmd == nil {
				t.Fatal("no command returned; want the clipboard read")
			}
			msg := cmd()
			pasted, ok := msg.(clipboardPastedMsg)
			if !ok {
				t.Fatalf("cmd returned %T, want clipboardPastedMsg", msg)
			}
			if pasted.text != "PASTED" {
				t.Errorf("pasted text = %q, want %q", pasted.text, "PASTED")
			}
		})
	}
}

// TestHandleKey_EveryDispatchedActionHasACaseArm reads handleKey's source and
// checks that each registered action appears as a case label.
//
// It is a STATIC check and says nothing about what a body does. It exists
// because the failure it catches is otherwise silent: an action whose arm was
// dropped in the move to the registry falls through to the PTY-forward default,
// so the key types an escape sequence into the shell instead of erroring. Every
// arm's behaviour is covered by the pre-existing keybinding tests in this
// package, which the refactor left untouched.
//
// json.transform must have NO arm: it is Hidden precisely because M5 never
// built a handler, and adding one would be new behaviour.
func TestHandleKey_EveryDispatchedActionHasACaseArm(t *testing.T) {
	src, err := os.ReadFile("model.go")
	if err != nil {
		t.Fatalf("read model.go: %v", err)
	}
	body := handleKeySource(t, string(src))

	var checked int
	for _, a := range keymap.Actions() {
		label := "case \"" + string(a.ID) + "\""
		has := strings.Contains(body, label) ||
			strings.Contains(body, ", \""+string(a.ID)+"\":") // shared arm
		if a.ID == "json.transform" {
			if has {
				t.Error("json.transform grew a dispatch arm; it has no handler by design")
			}
			continue
		}
		if !has {
			t.Errorf("action %q has no case arm in handleKey — its key falls through to the PTY", a.ID)
		}
		checked++
	}
	if checked != len(keymap.Actions())-1 {
		t.Errorf("checked %d actions, want %d", checked, len(keymap.Actions())-1)
	}
}

// TestKeymap_EveryShippedDefaultMatchesARealKeyPress closes the assumption the
// whole refactor rests on. kbMatches compared config text against
// tea.KeyPressMsg.String() directly, so whatever bubbletea rendered was by
// definition what the config had to say. MatchTier instead canonicalizes both
// sides through ParseChord, so a shipped default whose spelling bubbletea does
// not produce is now a silently dead key.
//
// keymap's own round-trip test only checks ParseChord against itself — it never
// builds a tea.KeyPressMsg — so nothing in the tree compared the two until now.
//
// The msg.String() assertion validates the fixture at the same time: a wrong
// Code fails loudly here instead of quietly asserting about the wrong key.
func TestKeymap_EveryShippedDefaultMatchesARealKeyPress(t *testing.T) {
	km, _ := buildKeymap(config.Default().Keybindings)
	specs := keySpecsFromConfig(config.Default().Keybindings)

	var checked int
	for _, a := range keymap.Actions() {
		for _, chord := range strings.Split(specs[a.ID], ",") {
			chord = strings.TrimSpace(chord)
			if chord == "" {
				continue // next_pane / prev_pane ship unbound
			}
			t.Run(string(a.ID)+"/"+chord, func(t *testing.T) {
				msg, ok := keyPressForChord(chord)
				if !ok {
					t.Fatalf("no tea.KeyPressMsg encoding for %q — extend keyPressForChord", chord)
				}
				if got := msg.String(); got != chord {
					t.Fatalf("bubbletea renders this key as %q but the shipped default spells it %q — "+
						"either the fixture's Code is wrong or the default binding is dead", got, chord)
				}
				got, found := km.MatchTier(a.Tier, msg.String())
				if !found || got != a.ID {
					t.Errorf("a real %q press resolves to (%q,%v), want (%q,true)", chord, got, found, a.ID)
				}
			})
			checked++
		}
	}
	if checked < len(keymap.Actions())-2 { // next_pane and prev_pane ship unbound
		t.Errorf("only %d chords checked across %d actions — the walk is skipping bindings",
			checked, len(keymap.Actions()))
	}
}

// keyPressForChord builds the tea.KeyPressMsg a terminal would deliver for a
// canonical chord. It returns false rather than guessing: a chord it cannot
// encode must fail its test, not silently pass one.
func keyPressForChord(chord string) (tea.KeyPressMsg, bool) {
	named := map[string]rune{
		"left": tea.KeyLeft, "right": tea.KeyRight, "up": tea.KeyUp, "down": tea.KeyDown,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "backspace": tea.KeyBackspace,
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "space": tea.KeySpace,
		"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3, "f4": tea.KeyF4,
		"f8": tea.KeyF8, "f9": tea.KeyF9,
	}
	var mod tea.KeyMod
	rest := chord
	for {
		switch {
		case strings.HasPrefix(rest, "ctrl+"):
			mod |= tea.ModCtrl
			rest = strings.TrimPrefix(rest, "ctrl+")
		case strings.HasPrefix(rest, "alt+"):
			mod |= tea.ModAlt
			rest = strings.TrimPrefix(rest, "alt+")
		case strings.HasPrefix(rest, "shift+"):
			mod |= tea.ModShift
			rest = strings.TrimPrefix(rest, "shift+")
		default:
			if code, ok := named[rest]; ok {
				return tea.KeyPressMsg{Code: code, Mod: mod}, true
			}
			if r := []rune(rest); len(r) == 1 {
				return tea.KeyPressMsg{Code: r[0], Mod: mod}, true
			}
			return tea.KeyPressMsg{}, false
		}
	}
}

// handleKeySource returns the text of handleKey, from its declaration to the
// declaration that follows it.
func handleKeySource(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "func (m Model) handleKey(")
	if start < 0 {
		t.Fatal("handleKey not found in model.go — this test needs updating")
	}
	rest := src[start+1:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("could not find the end of handleKey")
	}
	return rest[:end]
}
