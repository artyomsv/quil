package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

// keysModel is seqModel (two panes, real keymap) with an isolated QUIL_HOME
// holding the given bindings.toml, applied the way main.go applies it.
func keysModel(t *testing.T, file string) Model {
	t.Helper()
	t.Setenv("QUIL_HOME", t.TempDir())
	if file != "" {
		if err := os.WriteFile(config.BindingsPath(), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := seqModel(t, nil)
	disposeAtEnd(t, &m)
	b, err := config.LoadBindings()
	if err == nil {
		m.SetBindings(b)
	}
	return m
}

// openKeys drives F1 → Settings → Keys through the real Enter handler.
func openKeys(t *testing.T, m Model) Model {
	t.Helper()
	m.dialog = dialogSettings
	found := false
	for i, f := range settingsFields() {
		if f.keysSettings {
			m.dialogCursor, found = i, true
		}
	}
	if !found {
		t.Fatal("no Keys row in F1 → Settings")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog != dialogKeySettings {
		t.Fatalf("Enter on the Keys row opened dialog %v", m.dialog)
	}
	return m
}

// pressRun drives a key through Update and then runs every command it
// returned, feeding each resulting message back through Update — so a save
// that happens in a tea.Cmd is applied exactly as the program would.
func pressRun(t *testing.T, m Model, k tea.KeyPressMsg) Model {
	t.Helper()
	upd, cmd := m.Update(k)
	m = upd.(Model)
	for _, msg := range collectCmds(cmd) {
		if _, ok := msg.(keysSavedMsg); !ok {
			continue
		}
		upd, _ = m.Update(msg)
		m = upd.(Model)
	}
	return m
}

func collectCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectCmds(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func selectPreset(t *testing.T, m Model, name string) Model {
	t.Helper()
	m.dialogCursor = keysRowPreset
	for i := 0; i < len(keymap.PresetNames())+1; i++ {
		if m.keyDraft.preset == name {
			return m
		}
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	}
	t.Fatalf("preset %q never selected; draft is %q", name, m.keyDraft.preset)
	return m
}

func saveKeys(t *testing.T, m Model) Model {
	t.Helper()
	m.dialogCursor = keysRowSave
	return pressRun(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// AC-12 (TUI half): switching to tmux changes the keys at once, and the
// overrides in bindings.toml survive the rewrite.
func TestKeySettings_SwitchToTmuxAppliesAtOnceAndKeepsOverrides(t *testing.T) {
	m := keysModel(t, "preset = \"default\"\n[bindings]\n\"pane.mute\" = \"alt+shift+m\"\n")
	m = openKeys(t, m)
	m = selectPreset(t, m, "tmux")
	m = saveKeys(t, m)

	if m.bindings.Preset != "tmux" {
		t.Fatalf("applied preset = %q, want tmux", m.bindings.Preset)
	}
	if _, ok := m.keymap.MatchTier(keymap.TierLate, "ctrl+w"); ok {
		t.Error("ctrl+w still bound: the new preset was not applied")
	}
	b, err := config.LoadBindings()
	if err != nil {
		t.Fatal(err)
	}
	if b.Preset != "tmux" || b.Overrides["pane.mute"] != "alt+shift+m" {
		t.Errorf("file holds preset=%q overrides=%v, want tmux + the pane.mute override", b.Preset, b.Overrides)
	}
	if id, ok := m.keymap.MatchTier(keymap.TierEarly, "alt+shift+m"); !ok || id != "pane.mute" {
		t.Errorf("override lost from the live keymap: (%q,%v)", id, ok)
	}
}

// AC-10 (TUI half): after the live switch a tmux sequence runs. focus_toggle
// is used because its effect is local state (sequence_test.go:63-67).
func TestKeySettings_TmuxSequenceRunsAfterTheSwitch(t *testing.T) {
	m := keysModel(t, "")
	m = openKeys(t, m)
	m = selectPreset(t, m, "tmux")
	m = saveKeys(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // back to Settings
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // back to About
	m.dialog = dialogNone

	before := m.activeTabModel().FocusMode()
	m = press(t, m, ctrlB())
	m = press(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"})
	if m.activeTabModel().FocusMode() == before {
		t.Error("ctrl+b z did not toggle focus mode after the switch")
	}
}

// Apply cancels a pending prefix FIRST: a half-typed sequence of the old
// keymap must not complete against the new one. The save result is fed to
// Update directly (it is a message, not a key), so no key-path cancel can
// make this pass by accident.
func TestKeySettings_SaveCancelsAPendingSequence(t *testing.T) {
	m := keysModel(t, "preset = \"tmux\"\n")
	m = openKeys(t, m)
	m.pendingSeq = []keymap.Chord{{Mods: keymap.ModCtrl, Key: "b"}}
	gen := m.pendingGen
	upd, _ := m.Update(keysSavedMsg{b: config.Bindings{Preset: keymap.DefaultPresetName}})
	m = upd.(Model)
	if len(m.pendingSeq) != 0 || m.pendingGen == gen {
		t.Errorf("pendingSeq=%v gen %d→%d: the sequence was not cancelled", m.pendingSeq, gen, m.pendingGen)
	}
}

// A bindings.toml that does not parse is never overwritten: saving would
// replace the user's file with one holding none of their overrides.
func TestKeySettings_UnreadableFileIsNotOverwritten(t *testing.T) {
	const broken = "preset = \"default\"\n[bindings\n"
	m := keysModel(t, broken)
	before := m.keymap
	m = openKeys(t, m)
	m = selectPreset(t, m, "tmux")
	m = saveKeys(t, m)
	data, err := os.ReadFile(config.BindingsPath())
	if err != nil || string(data) != broken {
		t.Errorf("file changed to %q (%v), want it untouched", data, err)
	}
	if m.keymap != before {
		t.Error("keymap replaced although nothing was saved")
	}
	if !strings.Contains(m.keyStatus, "not saved") {
		t.Errorf("status %q does not say the save failed", m.keyStatus)
	}
}

// The prefix row takes only one chord; a refused value changes nothing.
func TestKeySettings_PrefixEditValidates(t *testing.T) {
	m := keysModel(t, "preset = \"tmux\"\n")
	m = openKeys(t, m)
	m.dialogCursor = keysRowPrefix
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.dialogEdit {
		t.Fatal("Enter on the prefix row did not start editing")
	}
	m.dialogInput = "ctrl+a, ctrl+b"
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.keyDraft.prefix != "" || !strings.Contains(m.keyStatus, "one chord") {
		t.Errorf("draft prefix %q status %q: a two-chord prefix was accepted", m.keyDraft.prefix, m.keyStatus)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.dialogInput = "ctrl+a"
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = saveKeys(t, m)
	if id, kind := m.keymap.MatchSeq([]keymap.Chord{{Mods: keymap.ModCtrl, Key: "a"}, {Key: "c"}}); kind != keymap.MatchExact || id != "tab.new" {
		t.Errorf("ctrl+a c = (%q,%v), want tab.new", id, kind)
	}
}

// The default preset uses no prefix, so the row says so and is skipped.
func TestKeySettings_PrefixRowInertForTheDefaultPreset(t *testing.T) {
	m := keysModel(t, "")
	m = openKeys(t, m)
	m.dialogCursor = keysRowPreset
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.dialogCursor != keysRowSave {
		t.Errorf("cursor = %d, want the Save row: the prefix row must be skipped", m.dialogCursor)
	}
	if frame := stripANSI(m.renderKeySettingsDialog()); !strings.Contains(frame, "not used") {
		t.Errorf("prefix row does not say it is unused:\n%s", frame)
	}
}

// Conflict lines are the draft's, shown before saving.
func TestKeySettings_ShowsTheDraftsConflicts(t *testing.T) {
	m := keysModel(t, "[bindings]\n\"pane.rename\" = \"ctrl+b\"\n")
	m = openKeys(t, m)
	m = selectPreset(t, m, "tmux")
	frame := stripANSI(m.renderKeySettingsDialog())
	if !strings.Contains(frame, "unreachable binding") {
		t.Errorf("the tmux draft's shadow conflict is not shown:\n%s", frame)
	}
	if !strings.Contains(frame, "comments") {
		t.Errorf("the page does not warn that comments are not kept:\n%s", frame)
	}
}

// A paste outside prefix editing is swallowed: it would otherwise be typed
// into the pane behind the page.
func TestKeySettings_PasteOutsideEditDoesNotReachThePane(t *testing.T) {
	m := keysModel(t, "")
	m = openKeys(t, m)
	upd, cmd := m.Update(tea.PasteMsg{Content: "rm -rf x\r"})
	m = upd.(Model)
	if cmd != nil {
		t.Error("paste returned a command; it must be dropped")
	}
	if m.dialog != dialogKeySettings {
		t.Errorf("dialog = %v after a paste", m.dialog)
	}
}

// A prefix chosen for tmux does not outlive a switch back to default: the
// default preset has no ${prefix}, so the file must not keep one that would
// silently return with the next tmux switch.
func TestKeySettings_SwitchingBackToDefaultClearsThePrefix(t *testing.T) {
	m := keysModel(t, "preset = \"tmux\"\nprefix = \"ctrl+a\"\n")
	m = openKeys(t, m)
	if m.keyDraft.prefix != "ctrl+a" {
		t.Fatalf("draft prefix = %q, want the file's ctrl+a", m.keyDraft.prefix)
	}
	m = selectPreset(t, m, keymap.DefaultPresetName)
	m = saveKeys(t, m)
	b, err := config.LoadBindings()
	if err != nil {
		t.Fatal(err)
	}
	if b.Preset != keymap.DefaultPresetName || b.Prefix != "" {
		t.Errorf("file holds preset=%q prefix=%q, want default and no prefix", b.Preset, b.Prefix)
	}
}

// An unknown preset in the file fell back to the default live; the draft must
// start from that, or Save would write the unknown name back.
func TestKeySettings_UnknownPresetStartsTheDraftFromTheLiveOne(t *testing.T) {
	m := keysModel(t, "preset = \"no-such\"\n")
	m = openKeys(t, m)
	if m.keyDraft.preset != keymap.DefaultPresetName {
		t.Errorf("draft preset = %q, want %q", m.keyDraft.preset, keymap.DefaultPresetName)
	}
}

// The conflict preview reads the file as Save will, not as it was at launch.
func TestKeySettings_PreviewUsesTheFileNotTheLaunchSnapshot(t *testing.T) {
	m := keysModel(t, "")
	if err := os.WriteFile(config.BindingsPath(), []byte("[bindings]\n\"pane.rename\" = \"ctrl+b\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = openKeys(t, m)
	m = selectPreset(t, m, "tmux")
	if frame := stripANSI(m.renderKeySettingsDialog()); !strings.Contains(frame, "unreachable binding") {
		t.Errorf("an override added after launch is missing from the preview:\n%s", frame)
	}
}

func TestConflictLines_CapsAndCounts(t *testing.T) {
	cs := make([]keymap.Conflict, maxKeyConflictLines+3)
	lines := conflictLines(cs, 80)
	if len(lines) != maxKeyConflictLines+1 || lines[len(lines)-1] != "+3 more" {
		t.Errorf("got %d lines, last %q; want %d lines ending in \"+3 more\"", len(lines), lines[len(lines)-1], maxKeyConflictLines+1)
	}
	if got := conflictLines(cs[:2], 80); len(got) != 2 {
		t.Errorf("under the cap got %d lines, want 2", len(got))
	}
}
