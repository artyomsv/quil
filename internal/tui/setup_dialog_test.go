package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/plugin"
)

func TestValidateAndNormalizeCWD(t *testing.T) {
	// Create one tmp dir to reuse as the canonical "valid dir" case.
	validDir := t.TempDir()

	// Create a file (not a dir) to test the "not a directory" branch.
	notDirPath := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(notDirPath, []byte{}, 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	cases := []struct {
		name       string
		input      string
		wantErrSub string // empty = no error; otherwise substring expected in error
		wantAbs    string // if non-empty, the cleaned path must equal this
	}{
		{"empty accepted", "", "", ""},
		{"whitespace only accepted", "   ", "", ""},
		{"quotes only accepted", `""`, "", ""},
		{"valid tmpdir", validDir, "", validDir},
		{"quoted valid tmpdir", `"` + validDir + `"`, "", validDir},
		{"trailing whitespace", validDir + "   ", "", validDir},
		{"tilde alone resolves to home", "~", "", home},
		{"nonexistent", filepath.Join(os.TempDir(), "definitely-not-here-xyz-9999"), "does not exist", ""},
		{"file not dir", notDirPath, "not a directory", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateAndNormalizeCWD(tc.input)
			if tc.wantErrSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.wantAbs != "" && got != tc.wantAbs {
					t.Errorf("got %q, want %q", got, tc.wantAbs)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got none", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

func TestSanitizePastedPath(t *testing.T) {
	cases := map[string]string{
		`  /foo/bar  `:  "/foo/bar",
		`"/foo/bar"`:    "/foo/bar",
		`  "/foo" `:     "/foo",
		`/no/changes`:   "/no/changes",
		``:              "",
		`"  "`:          "  ", // inner whitespace preserved, only outer trimmed
	}
	for in, want := range cases {
		if got := sanitizePastedPath(in); got != want {
			t.Errorf("sanitizePastedPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetupFieldKind_AndCount(t *testing.T) {
	type want struct {
		count int
		kinds []string // one entry per cursor index
	}

	cases := []struct {
		name string
		p    *plugin.PanePlugin
		want want
	}{
		{
			name: "cwd only",
			p: &plugin.PanePlugin{Command: plugin.CommandConfig{
				PromptsCWD: true,
			}},
			want: want{count: 2, kinds: []string{"cwd", "continue"}},
		},
		{
			name: "one toggle only",
			p: &plugin.PanePlugin{Command: plugin.CommandConfig{
				Toggles: []plugin.Toggle{{Name: "a"}},
			}},
			want: want{count: 2, kinds: []string{"toggle", "continue"}},
		},
		{
			name: "cwd + two toggles",
			p: &plugin.PanePlugin{Command: plugin.CommandConfig{
				PromptsCWD: true,
				Toggles:    []plugin.Toggle{{Name: "a"}, {Name: "b"}},
			}},
			want: want{count: 4, kinds: []string{"cwd", "toggle", "toggle", "continue"}},
		},
	}

	m := Model{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if n := m.setupFieldCount(tc.p); n != tc.want.count {
				t.Errorf("count = %d, want %d", n, tc.want.count)
			}
			for i, wantKind := range tc.want.kinds {
				kind, _ := m.setupFieldKind(tc.p, i)
				if kind != wantKind {
					t.Errorf("kind at index %d = %q, want %q", i, kind, wantKind)
				}
			}
		})
	}
}

func TestEnterSetupOrSplit_RoutingAndDefaults(t *testing.T) {
	pluginCWD := &plugin.PanePlugin{
		Name: "cwd-only",
		Command: plugin.CommandConfig{PromptsCWD: true},
	}
	pluginToggleOnly := &plugin.PanePlugin{
		Name: "toggle-only",
		Command: plugin.CommandConfig{Toggles: []plugin.Toggle{
			{Name: "safe", Default: false, ArgsWhenOn: []string{"--safe"}},
			{Name: "verbose", Default: true, ArgsWhenOn: []string{"-v"}},
		}},
	}
	pluginNeither := &plugin.PanePlugin{
		Name: "plain",
		Command: plugin.CommandConfig{},
	}

	t.Run("no setup — advances to split step 3", func(t *testing.T) {
		m := &Model{}
		cmd := m.enterSetupOrSplit(pluginNeither)
		if cmd != nil {
			t.Errorf("expected nil cmd (no dialog transition), got non-nil")
		}
		if m.createPaneStep != 3 {
			t.Errorf("expected createPaneStep = 3, got %d", m.createPaneStep)
		}
		if m.dialog == dialogCreatePaneSetup {
			t.Errorf("expected no setup dialog")
		}
	})

	t.Run("cwd only — opens setup with browser pre-loaded from home", func(t *testing.T) {
		m := &Model{}
		cmd := m.enterSetupOrSplit(pluginCWD)
		if cmd == nil {
			t.Error("expected non-nil cmd (ClearScreen) when opening dialog")
		}
		if m.dialog != dialogCreatePaneSetup {
			t.Errorf("expected dialog = dialogCreatePaneSetup, got %v", m.dialog)
		}
		if m.dialogEdit {
			t.Error("expected dialogEdit = false — browser doesn't use edit mode")
		}
		if m.setupFieldCursor != 0 {
			t.Errorf("expected cursor at 0 (CWD), got %d", m.setupFieldCursor)
		}
		// With no active pane, the browser falls back to user home and
		// pre-loads its directory listing. cwdBrowseDir should be set.
		if m.cwdBrowseDir == "" {
			t.Error("expected cwdBrowseDir to be set from user home fallback")
		}
	})

	t.Run("toggle only — opens setup, no browser, defaults applied", func(t *testing.T) {
		m := &Model{}
		cmd := m.enterSetupOrSplit(pluginToggleOnly)
		if cmd == nil {
			t.Error("expected non-nil cmd")
		}
		if m.dialog != dialogCreatePaneSetup {
			t.Errorf("expected dialog = dialogCreatePaneSetup")
		}
		if m.dialogEdit {
			t.Error("expected dialogEdit = false")
		}
		if m.cwdBrowseDir != "" {
			t.Errorf("expected no browser dir for toggle-only plugin, got %q", m.cwdBrowseDir)
		}
		if len(m.toggleStates) != 2 {
			t.Fatalf("expected 2 toggle states, got %d", len(m.toggleStates))
		}
		if m.toggleStates[0] != false {
			t.Error("toggle 'safe' default should be false")
		}
		if m.toggleStates[1] != true {
			t.Error("toggle 'verbose' default should be true")
		}
	})

	t.Run("cwd — lastSelectedCWD wins over home fallback", func(t *testing.T) {
		remembered := t.TempDir()
		m := &Model{lastSelectedCWD: remembered}
		m.enterSetupOrSplit(pluginCWD)
		if m.cwdBrowseDir != remembered {
			t.Errorf("expected cwdBrowseDir = %q (from lastSelectedCWD), got %q", remembered, m.cwdBrowseDir)
		}
	})

	t.Run("cwd — stale lastSelectedCWD cleared and falls through", func(t *testing.T) {
		stale := filepath.Join(t.TempDir(), "gone")
		m := &Model{lastSelectedCWD: stale}
		m.enterSetupOrSplit(pluginCWD)
		if m.lastSelectedCWD != "" {
			t.Errorf("expected lastSelectedCWD cleared after stale dir, got %q", m.lastSelectedCWD)
		}
		// Should have fallen through to home directory
		if m.cwdBrowseDir == "" {
			t.Error("expected cwdBrowseDir to be set from home fallback after stale dir")
		}
		if m.cwdBrowseDir == stale {
			t.Error("cwdBrowseDir should not be the stale directory")
		}
	})
}

// TestLoadBrowseDir verifies the directory browser populates correctly,
// prepends ".." for non-root paths, sorts entries, and skips files.
func TestLoadBrowseDir(t *testing.T) {
	root := t.TempDir()

	// Set up a known structure: 3 dirs (banana, apple, cherry — to verify sort)
	// + 1 file (which must NOT appear in the listing).
	for _, name := range []string{"banana", "apple", "cherry"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ignore_me.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := &Model{}
	if err := m.loadBrowseDir(root); err != nil {
		t.Fatalf("loadBrowseDir: %v", err)
	}

	// Expected: ".." first (non-root), then sorted dirs, no file.
	want := []string{"..", "apple", "banana", "cherry"}
	if len(m.cwdBrowseEntries) != len(want) {
		t.Fatalf("entries = %v, want %v", m.cwdBrowseEntries, want)
	}
	for i, w := range want {
		if m.cwdBrowseEntries[i] != w {
			t.Errorf("entries[%d] = %q, want %q", i, m.cwdBrowseEntries[i], w)
		}
	}
	if m.cwdBrowseCursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cwdBrowseCursor)
	}
}

// TestAdjustBrowseScroll verifies the visible-window math keeps the cursor
// inside the viewport for both upward and downward navigation.
func TestAdjustBrowseScroll(t *testing.T) {
	m := &Model{
		cwdBrowseEntries: make([]string, 30),
	}
	for i := range m.cwdBrowseEntries {
		m.cwdBrowseEntries[i] = "x"
	}

	// Cursor starts at 0 → scroll stays at 0.
	m.adjustBrowseScroll()
	if m.cwdBrowseScroll != 0 {
		t.Errorf("initial scroll = %d, want 0", m.cwdBrowseScroll)
	}

	// Cursor at the bottom of the visible window → scroll unchanged.
	m.cwdBrowseCursor = browserVisibleRows - 1
	m.adjustBrowseScroll()
	if m.cwdBrowseScroll != 0 {
		t.Errorf("scroll = %d, want 0 when cursor at bottom of first window", m.cwdBrowseScroll)
	}

	// Cursor steps below the visible window → scroll advances.
	m.cwdBrowseCursor = browserVisibleRows
	m.adjustBrowseScroll()
	if m.cwdBrowseScroll != 1 {
		t.Errorf("scroll = %d, want 1 after stepping past visible window", m.cwdBrowseScroll)
	}

	// Cursor jumps far down → scroll catches up.
	m.cwdBrowseCursor = 25
	m.adjustBrowseScroll()
	wantScroll := 25 - browserVisibleRows + 1
	if m.cwdBrowseScroll != wantScroll {
		t.Errorf("scroll = %d, want %d", m.cwdBrowseScroll, wantScroll)
	}

	// Cursor jumps back to top → scroll snaps back.
	m.cwdBrowseCursor = 0
	m.adjustBrowseScroll()
	if m.cwdBrowseScroll != 0 {
		t.Errorf("scroll = %d, want 0 after jumping to top", m.cwdBrowseScroll)
	}
}

// TestSetupDialog_PathValidationOnDifferentOS is a sanity check that the validator
// behaves sensibly on the host platform (Windows paths with backslashes, etc.).
// It doesn't try to be exhaustive — just confirms the validator doesn't reject
// a known-good path from the current OS.
func TestSetupDialog_PathValidationOnDifferentOS(t *testing.T) {
	tmp := t.TempDir()
	cleaned, err := validateAndNormalizeCWD(tmp)
	if err != nil {
		t.Fatalf("validator rejected t.TempDir(): %v", err)
	}
	// Compare against the symlink-resolved expected path. The validator now
	// runs filepath.EvalSymlinks so the result may differ from filepath.Abs
	// on systems where /tmp (or %TEMP%) is itself a symlink (macOS, some
	// Linux containers).
	abs, _ := filepath.Abs(tmp)
	expected, evalErr := filepath.EvalSymlinks(abs)
	if evalErr != nil {
		expected = abs
	}
	if cleaned != expected {
		t.Errorf("cleaned path %q != expected %q", cleaned, expected)
	}

	// Also sanity-check on Windows that forward-slash separators still validate.
	if runtime.GOOS == "windows" {
		fwd := filepath.ToSlash(tmp)
		if _, err := validateAndNormalizeCWD(fwd); err != nil {
			t.Errorf("validator rejected forward-slash path on Windows: %v", err)
		}
	}
}

// TestSanitizePastedPath_StripsControlBytes guards the S1 security fix:
// clipboard payloads with embedded OSC/CSI escape sequences must NOT survive
// past sanitizePastedPath, otherwise an os.Stat error message that quotes the
// input could inject terminal escapes into the rendered cwdInputError.
func TestSanitizePastedPath_StripsControlBytes(t *testing.T) {
	cases := map[string]string{
		// ESC (0x1b) and BEL (0x07) are stripped; "]", "0", ";" etc. are
		// printable ASCII and stay, but the OSC/CSI framing is broken.
		"\x1b]0;evil\x07/foo/bar": "]0;evil/foo/bar",
		"/foo/\x00bar":            "/foo/bar",  // NUL stripped
		// \r is trimmed by TrimSpace at the end, \n trimmed at the end too.
		"/foo/\rbar":      "/foo/bar",  // \r stripped mid-string
		"\x7fdel":         "del",       // DEL (0x7f) stripped
		"'/foo/bar'":      "/foo/bar",  // single quotes stripped
		"\x01\x02\x03/foo": "/foo",     // misc control bytes stripped
		"/foo\tbar":       "/foo\tbar", // tab preserved
	}
	for in, want := range cases {
		if got := sanitizePastedPath(in); got != want {
			t.Errorf("sanitizePastedPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKeyToBytes_ShiftTab_ReturnsCSI_Z guards the keyToBytes addition that
// powers shift+tab passthrough to Claude Code. Without this mapping, even a
// raw_keys-declared shift+tab would silently produce nil bytes.
func TestKeyToBytes_ShiftTab_ReturnsCSI_Z(t *testing.T) {
	got := keyToBytes(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if string(got) != "\x1b[Z" {
		t.Errorf("keyToBytes(shift+tab) = %q, want %q", string(got), "\x1b[Z")
	}
}

// TestIsSelectionExtendKey guards the guard: the pane-input router calls
// isSelectionExtendKey to decide whether to route a key into the scrollback
// selection handler, and any false positive here silently swallows keys that
// must reach the PTY (shift+tab for Claude Code mode toggle, shift+enter for
// multiline input, shift+Fn for TUI shortcuts).
func TestIsSelectionExtendKey(t *testing.T) {
	selection := []string{
		"shift+left", "shift+right", "shift+up", "shift+down",
		"ctrl+shift+left", "ctrl+shift+right",
		"ctrl+alt+shift+left", "ctrl+alt+shift+right",
	}
	mustForward := []string{
		"shift+tab",
		"shift+enter",
		"shift+home",
		"shift+end",
		"shift+f1",
		"shift+pgup",
		"shift+pgdown",
		"ctrl+shift+tab",
		"ctrl+shift+up", // handler has no case for this — would fall into default and clear selection
		"ctrl+shift+down",
		"tab",
		"enter",
		"ctrl+v",
	}
	for _, k := range selection {
		if !isSelectionExtendKey(k) {
			t.Errorf("isSelectionExtendKey(%q) = false, want true (arrow-based selection key)", k)
		}
	}
	for _, k := range mustForward {
		if isSelectionExtendKey(k) {
			t.Errorf("isSelectionExtendKey(%q) = true, want false (must reach PTY / other handler)", k)
		}
	}
}

// TestTryPluginRawKey verifies that tryPluginRawKey forwards keys declared in
// a plugin's RawKeys list and returns nil for any other key. The active pane's
// type drives the lookup.
//
// Quil's default plugins (terminal, claude-code, ssh, stripe) no longer opt
// into raw_keys — Tab and Shift+Tab reach the PTY naturally now that pane
// navigation lives on Alt+Arrow. So the test builds a synthetic plugin via
// a temp TOML to exercise the mechanism.
func TestTryPluginRawKey(t *testing.T) {
	// Load a synthetic "rawkey-test" plugin that declares shift+tab.
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "rawkey-test.toml")
	content := `[plugin]
name = "rawkey-test"
display_name = "Raw Key Test"
category = "test"

[command]
cmd = "true"
raw_keys = ["shift+tab"]
`
	if err := os.WriteFile(tomlPath, []byte(content), 0644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	// Construct a Model with a single tab containing a single pane whose
	// Type matches the synthetic plugin.
	pane := &PaneModel{ID: "p1", Name: "p1", Type: "rawkey-test", Active: true}
	tab := &TabModel{ID: "t1", Name: "Shell", ActivePane: pane.ID, Root: NewLeaf(pane)}
	m := Model{
		tabs:           []*TabModel{tab},
		activeTab:      0,
		pluginRegistry: r,
	}

	t.Run("declared key returns CSI Z", func(t *testing.T) {
		got := m.tryPluginRawKey("shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		if string(got) != "\x1b[Z" {
			t.Errorf("got %q, want \x1b[Z", string(got))
		}
	})

	t.Run("undeclared key returns nil", func(t *testing.T) {
		got := m.tryPluginRawKey("ctrl+a", tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
		if got != nil {
			t.Errorf("got %q, want nil", string(got))
		}
	})

	t.Run("plain printable key returns nil", func(t *testing.T) {
		got := m.tryPluginRawKey("a", tea.KeyPressMsg{Text: "a"})
		if got != nil {
			t.Errorf("got %q, want nil", string(got))
		}
	})

	t.Run("terminal pane does not opt in — returns nil", func(t *testing.T) {
		// Swap the pane's type to terminal (the builtin has no RawKeys).
		pane.Type = "terminal"
		defer func() { pane.Type = "rawkey-test" }()
		got := m.tryPluginRawKey("shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		if got != nil {
			t.Errorf("terminal pane should NOT forward shift+tab via raw_keys, got %q", string(got))
		}
	})

	t.Run("no active pane returns nil", func(t *testing.T) {
		empty := Model{pluginRegistry: r}
		if got := empty.tryPluginRawKey("shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}); got != nil {
			t.Errorf("got %q, want nil with no active pane", string(got))
		}
	})
}

// TestSubmitSetupDialog_AppendsToggleArgsAndCommitsCWD verifies the critical
// path that threads user choices through to CreatePanePayload: cwdBrowseDir
// is copied into selectedCWD, and enabled-toggle args are appended to
// selectedInstanceArgs without dropping prior args.
func TestSubmitSetupDialog_AppendsToggleArgsAndCommitsCWD(t *testing.T) {
	p := &plugin.PanePlugin{
		Name: "claude-code",
		Command: plugin.CommandConfig{
			PromptsCWD: true,
			Toggles: []plugin.Toggle{
				{Name: "skip", Default: false, ArgsWhenOn: []string{"--dangerously-skip-permissions"}},
				{Name: "verbose", Default: false, ArgsWhenOn: []string{"-v"}},
			},
		},
	}

	t.Run("toggles off — selectedInstanceArgs untouched, CWD copied", func(t *testing.T) {
		m := Model{
			selectedInstanceArgs: []string{"--existing"},
			toggleStates:         []bool{false, false},
			cwdBrowseDir:         "/home/user/proj",
		}
		next, _ := m.submitSetupDialog(p)
		m = next.(Model)

		if m.selectedCWD != "/home/user/proj" {
			t.Errorf("selectedCWD = %q, want /home/user/proj", m.selectedCWD)
		}
		want := []string{"--existing"}
		if !reflect.DeepEqual(m.selectedInstanceArgs, want) {
			t.Errorf("selectedInstanceArgs = %v, want %v", m.selectedInstanceArgs, want)
		}
	})

	t.Run("only first toggle on — its args appended", func(t *testing.T) {
		m := Model{
			selectedInstanceArgs: nil,
			toggleStates:         []bool{true, false},
			cwdBrowseDir:         "/home/user/proj",
		}
		next, _ := m.submitSetupDialog(p)
		m = next.(Model)
		want := []string{"--dangerously-skip-permissions"}
		if !reflect.DeepEqual(m.selectedInstanceArgs, want) {
			t.Errorf("selectedInstanceArgs = %v, want %v", m.selectedInstanceArgs, want)
		}
	})

	t.Run("multiple toggles + pre-existing instance args — order preserved", func(t *testing.T) {
		m := Model{
			selectedInstanceArgs: []string{"--model", "opus"},
			toggleStates:         []bool{true, true},
			cwdBrowseDir:         "/home/user/proj",
		}
		next, _ := m.submitSetupDialog(p)
		m = next.(Model)
		want := []string{"--model", "opus", "--dangerously-skip-permissions", "-v"}
		if !reflect.DeepEqual(m.selectedInstanceArgs, want) {
			t.Errorf("selectedInstanceArgs = %v, want %v", m.selectedInstanceArgs, want)
		}
	})

	t.Run("PromptsCWD false — selectedCWD not touched even if browser populated", func(t *testing.T) {
		pNoCWD := &plugin.PanePlugin{
			Command: plugin.CommandConfig{
				Toggles: []plugin.Toggle{{Name: "x", ArgsWhenOn: []string{"-x"}}},
			},
		}
		m := Model{
			toggleStates: []bool{true},
			cwdBrowseDir: "/should/not/leak",
			selectedCWD:  "",
		}
		next, _ := m.submitSetupDialog(pNoCWD)
		m = next.(Model)
		if m.selectedCWD != "" {
			t.Errorf("selectedCWD = %q, want empty (PromptsCWD off)", m.selectedCWD)
		}
	})

	t.Run("after submit, dialog and step advance to split direction", func(t *testing.T) {
		m := Model{
			toggleStates: []bool{false, false},
			cwdBrowseDir: "/x",
		}
		next, _ := m.submitSetupDialog(p)
		m = next.(Model)
		if m.dialog != dialogCreatePane {
			t.Errorf("dialog = %v, want dialogCreatePane", m.dialog)
		}
		if m.createPaneStep != 3 {
			t.Errorf("createPaneStep = %d, want 3", m.createPaneStep)
		}
	})
}

// TestEnforceToggleGroups covers the mutual-exclusion invariant that
// backs the radio-button UX for toggles sharing a non-empty Group value.
// Ungrouped toggles must never be touched; grouped toggles must collapse
// to at most one ON member per group.
func TestEnforceToggleGroups(t *testing.T) {
	t.Run("ungrouped toggles untouched when winner is in a group", func(t *testing.T) {
		toggles := []plugin.Toggle{
			{Name: "skip", Group: "permission_mode"},
			{Name: "auto", Group: "permission_mode"},
			{Name: "verbose", Group: ""},
		}
		states := []bool{true, false, true}
		enforceToggleGroups(toggles, states, 0)
		want := []bool{true, false, true} // verbose (ungrouped) untouched
		if !reflect.DeepEqual(states, want) {
			t.Errorf("states = %v, want %v", states, want)
		}
	})

	t.Run("winner turns off other group members", func(t *testing.T) {
		toggles := []plugin.Toggle{
			{Name: "skip", Group: "permission_mode"},
			{Name: "auto", Group: "permission_mode"},
		}
		states := []bool{true, true} // invalid pre-state; winner = auto (idx 1)
		enforceToggleGroups(toggles, states, 1)
		want := []bool{false, true}
		if !reflect.DeepEqual(states, want) {
			t.Errorf("states = %v, want %v", states, want)
		}
	})

	t.Run("winner with empty group is a no-op", func(t *testing.T) {
		toggles := []plugin.Toggle{
			{Name: "a", Group: ""},
			{Name: "b", Group: "grp"},
		}
		states := []bool{true, true}
		enforceToggleGroups(toggles, states, 0) // winner is ungrouped
		want := []bool{true, true}
		if !reflect.DeepEqual(states, want) {
			t.Errorf("states = %v, want %v", states, want)
		}
	})

	t.Run("no explicit winner — last true per group wins", func(t *testing.T) {
		toggles := []plugin.Toggle{
			{Name: "first", Group: "g"},
			{Name: "second", Group: "g"},
			{Name: "third", Group: "g"},
		}
		states := []bool{true, false, true} // first and third both claim
		enforceToggleGroups(toggles, states, -1)
		want := []bool{false, false, true} // third (last declared) wins
		if !reflect.DeepEqual(states, want) {
			t.Errorf("states = %v, want %v", states, want)
		}
	})

	t.Run("multiple groups are independent", func(t *testing.T) {
		toggles := []plugin.Toggle{
			{Name: "a1", Group: "A"},
			{Name: "a2", Group: "A"},
			{Name: "b1", Group: "B"},
			{Name: "b2", Group: "B"},
		}
		states := []bool{true, false, false, true}
		enforceToggleGroups(toggles, states, 0) // winner in A, B untouched
		want := []bool{true, false, false, true}
		if !reflect.DeepEqual(states, want) {
			t.Errorf("states = %v, want %v", states, want)
		}
	})

	t.Run("safe with empty / short inputs", func(t *testing.T) {
		// Should not panic with any of these.
		enforceToggleGroups(nil, nil, -1)
		enforceToggleGroups(nil, []bool{true}, 0)
		enforceToggleGroups([]plugin.Toggle{{Name: "x", Group: "g"}}, nil, 0)
		// Winner index out of range — stays a no-op.
		s := []bool{true}
		enforceToggleGroups([]plugin.Toggle{{Name: "x", Group: "g"}}, s, 99)
		if !s[0] {
			t.Error("state should not be mutated when winner index is out of range")
		}
	})
}

// TestEnterSetupOrSplit_PermissionGroup_SingleDefaultHonoured guards the
// group invariant on dialog open: even if TOML declares both members of a
// group with default = true (misconfiguration), only one is ON initially.
func TestEnterSetupOrSplit_PermissionGroup_SingleDefaultHonoured(t *testing.T) {
	p := &plugin.PanePlugin{
		Name: "claude-code",
		Command: plugin.CommandConfig{
			Toggles: []plugin.Toggle{
				{Name: "skip", Default: true, ArgsWhenOn: []string{"--dangerously-skip-permissions"}, Group: "permission_mode"},
				{Name: "auto", Default: true, ArgsWhenOn: []string{"--enable-auto-mode"}, Group: "permission_mode"},
			},
		},
	}
	m := &Model{}
	m.enterSetupOrSplit(p)
	if len(m.toggleStates) != 2 {
		t.Fatalf("expected 2 toggle states, got %d", len(m.toggleStates))
	}
	onCount := 0
	for _, s := range m.toggleStates {
		if s {
			onCount++
		}
	}
	if onCount > 1 {
		t.Errorf("expected at most 1 toggle ON in permission_mode group, got %d", onCount)
	}
	// Last-declared default-true wins: "auto".
	if !m.toggleStates[1] || m.toggleStates[0] {
		t.Errorf("expected toggleStates = [false, true] (last-declared wins), got %v", m.toggleStates)
	}
}

// TestHandleCreatePaneSetupKey_GroupMutualExclusion drives the dialog FSM
// to confirm that toggling one member of a group ON forces the other
// member OFF, and that pressing Space again to toggle OFF leaves the
// group fully unselected (a valid state).
func TestHandleCreatePaneSetupKey_GroupMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "claude-code.toml")
	content := `[plugin]
name = "claude-code"
display_name = "Claude Code"
category = "ai"

[command]
cmd = "true"

[[command.toggles]]
name = "skip"
label = "Skip permissions"
args_when_on = ["--dangerously-skip-permissions"]
default = false
group = "permission_mode"

[[command.toggles]]
name = "auto"
label = "Enable auto mode"
args_when_on = ["--enable-auto-mode"]
default = false
group = "permission_mode"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	m := Model{
		pluginRegistry:   r,
		selectedPlugin:   "claude-code",
		dialog:           dialogCreatePaneSetup,
		toggleStates:     []bool{false, false},
		setupFieldCursor: 0, // first toggle (no CWD field in this synthetic plugin)
	}

	space := tea.KeyPressMsg{Code: ' ', Text: " "}

	// Turn first toggle ON.
	next, _ := m.handleCreatePaneSetupKey(space)
	m = next.(Model)
	if !(m.toggleStates[0] && !m.toggleStates[1]) {
		t.Fatalf("after enabling skip: states = %v, want [true, false]", m.toggleStates)
	}

	// Move cursor to second toggle and turn it ON — first must flip OFF.
	m.setupFieldCursor = 1
	next, _ = m.handleCreatePaneSetupKey(space)
	m = next.(Model)
	if !(!m.toggleStates[0] && m.toggleStates[1]) {
		t.Errorf("after enabling auto: states = %v, want [false, true] (skip should auto-disable)", m.toggleStates)
	}

	// Pressing Space again on the second toggle should turn it OFF —
	// neither is selected now, which is a valid state ("pick neither").
	next, _ = m.handleCreatePaneSetupKey(space)
	m = next.(Model)
	if m.toggleStates[0] || m.toggleStates[1] {
		t.Errorf("after turning auto off: states = %v, want [false, false]", m.toggleStates)
	}
}

// TestEnterSetupOrSplit_ClearsLeftoverState_OnNoSetupBranch guards the Q2 fix:
// when the next plugin has no setup, the early return must STILL clear stale
// state from a prior plugin (otherwise a CWD selected for plugin A leaks into
// the spawn for plugin B after Esc-then-pick-different-plugin).
func TestEnterSetupOrSplit_ClearsLeftoverState_OnNoSetupBranch(t *testing.T) {
	plain := &plugin.PanePlugin{
		Name:    "plain",
		Command: plugin.CommandConfig{},
	}

	m := &Model{
		// Simulate "user committed setup for a previous plugin"
		selectedCWD:      "/leftover/from/prev/plugin",
		cwdBrowseDir:     "/leftover/from/prev/plugin",
		toggleStates:     []bool{true, false},
		setupFieldCursor: 2,
		cwdInputError:    "stale error",
	}
	cmd := m.enterSetupOrSplit(plain)
	if cmd != nil {
		t.Errorf("expected nil cmd for no-setup plugin, got non-nil")
	}
	if m.selectedCWD != "" {
		t.Errorf("selectedCWD leaked: %q", m.selectedCWD)
	}
	if m.cwdBrowseDir != "" {
		t.Errorf("cwdBrowseDir leaked: %q", m.cwdBrowseDir)
	}
	if m.toggleStates != nil {
		t.Errorf("toggleStates leaked: %v", m.toggleStates)
	}
	if m.cwdInputError != "" {
		t.Errorf("cwdInputError leaked: %q", m.cwdInputError)
	}
	if m.setupFieldCursor != 0 {
		t.Errorf("setupFieldCursor leaked: %d", m.setupFieldCursor)
	}
	if m.createPaneStep != 3 {
		t.Errorf("createPaneStep = %d, want 3", m.createPaneStep)
	}
}

// TestHandleSetupCWDKey_BrowserNavigation drives the directory-browser FSM
// directly so the cursor / scroll / parent-up / paste branches are exercised
// without going through the dialog router. Each subtest sets up a fresh
// browser state with t.TempDir() and a plugin that has PromptsCWD = true.
func TestHandleSetupCWDKey_BrowserNavigation(t *testing.T) {
	p := &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{PromptsCWD: true},
	}

	// freshModel returns a Model with the browser pre-loaded with N synthetic
	// entries (".." + N-1 dirs). The cursor starts at 0.
	freshModel := func(n int) Model {
		entries := make([]string, 0, n)
		entries = append(entries, "..")
		for i := 1; i < n; i++ {
			entries = append(entries, fmt.Sprintf("d%02d", i))
		}
		return Model{
			cwdBrowseDir:     "/fake/root",
			cwdBrowseEntries: entries,
			cwdBrowseCursor:  0,
		}
	}

	t.Run("down advances cursor and clamps at end", func(t *testing.T) {
		m := freshModel(3)
		next, _ := m.handleSetupCWDKey(p, "down")
		m = next.(Model)
		if m.cwdBrowseCursor != 1 {
			t.Errorf("after down: cursor = %d, want 1", m.cwdBrowseCursor)
		}
		next, _ = m.handleSetupCWDKey(p, "down")
		m = next.(Model)
		next, _ = m.handleSetupCWDKey(p, "down") // already at last
		m = next.(Model)
		if m.cwdBrowseCursor != 2 {
			t.Errorf("after extra down: cursor = %d, want 2 (clamped)", m.cwdBrowseCursor)
		}
	})

	t.Run("up retreats cursor and clamps at top", func(t *testing.T) {
		m := freshModel(5)
		m.cwdBrowseCursor = 2
		next, _ := m.handleSetupCWDKey(p, "up")
		m = next.(Model)
		if m.cwdBrowseCursor != 1 {
			t.Errorf("after up: cursor = %d, want 1", m.cwdBrowseCursor)
		}
		next, _ = m.handleSetupCWDKey(p, "up")
		m = next.(Model)
		next, _ = m.handleSetupCWDKey(p, "up") // already at top
		m = next.(Model)
		if m.cwdBrowseCursor != 0 {
			t.Errorf("after extra up: cursor = %d, want 0 (clamped)", m.cwdBrowseCursor)
		}
	})

	t.Run("home jumps to first, end jumps to last", func(t *testing.T) {
		m := freshModel(10)
		m.cwdBrowseCursor = 5
		next, _ := m.handleSetupCWDKey(p, "home")
		m = next.(Model)
		if m.cwdBrowseCursor != 0 {
			t.Errorf("after home: cursor = %d, want 0", m.cwdBrowseCursor)
		}
		next, _ = m.handleSetupCWDKey(p, "end")
		m = next.(Model)
		if m.cwdBrowseCursor != 9 {
			t.Errorf("after end: cursor = %d, want 9", m.cwdBrowseCursor)
		}
	})

	t.Run("pgdown jumps by visible-rows", func(t *testing.T) {
		m := freshModel(50)
		next, _ := m.handleSetupCWDKey(p, "pgdown")
		m = next.(Model)
		if m.cwdBrowseCursor != browserVisibleRows {
			t.Errorf("after pgdown: cursor = %d, want %d", m.cwdBrowseCursor, browserVisibleRows)
		}
	})

	t.Run("enter on real subdir descends into it", func(t *testing.T) {
		// Real filesystem this time so loadBrowseDir succeeds.
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "child"), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		m := Model{}
		if err := m.loadBrowseDir(root); err != nil {
			t.Fatalf("loadBrowseDir: %v", err)
		}
		// Find "child" in the listing — it should be index 1 (after "..").
		for i, e := range m.cwdBrowseEntries {
			if e == "child" {
				m.cwdBrowseCursor = i
				break
			}
		}
		next, _ := m.handleSetupCWDKey(p, "enter")
		m = next.(Model)
		if filepath.Base(m.cwdBrowseDir) != "child" {
			t.Errorf("after enter on child: cwdBrowseDir = %q, want .../child", m.cwdBrowseDir)
		}
	})

	t.Run("backspace navigates to parent and highlights child we came from", func(t *testing.T) {
		root := t.TempDir()
		childPath := filepath.Join(root, "subdir")
		if err := os.Mkdir(childPath, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		m := Model{}
		if err := m.loadBrowseDir(childPath); err != nil {
			t.Fatalf("loadBrowseDir child: %v", err)
		}
		// In the child dir now. Press backspace → parent.
		next, _ := m.handleSetupCWDKey(p, "backspace")
		m = next.(Model)
		// EvalSymlinks may resolve macOS /tmp etc., so compare bases instead.
		if filepath.Base(m.cwdBrowseDir) != filepath.Base(root) {
			t.Errorf("after backspace: dir base = %q, want %q",
				filepath.Base(m.cwdBrowseDir), filepath.Base(root))
		}
		// Cursor should land on "subdir" (the child we came from).
		if m.cwdBrowseCursor < 0 || m.cwdBrowseCursor >= len(m.cwdBrowseEntries) ||
			m.cwdBrowseEntries[m.cwdBrowseCursor] != "subdir" {
			t.Errorf("cursor not positioned on child we came from: cursor=%d entries=%v",
				m.cwdBrowseCursor, m.cwdBrowseEntries)
		}
	})

	t.Run("empty browser submits via enter", func(t *testing.T) {
		m := Model{cwdBrowseEntries: nil}
		next, _ := m.handleSetupCWDKey(p, "enter")
		mNext := next.(Model)
		// Empty entries + Enter routes through submitSetupDialog → step 3.
		if mNext.createPaneStep != 3 {
			t.Errorf("empty browser + enter: createPaneStep = %d, want 3", mNext.createPaneStep)
		}
	})
}

// TestHandleCreatePaneSetupKey_Routing exercises the field-cursor FSM that
// sits on top of handleSetupCWDKey: Tab/Shift+Tab cycle the focused field,
// Esc unwinds, Space toggles a checkbox, and Enter on Continue submits.
// Uses a real registry built from a synthetic TOML so pluginRegistry.Get
// returns a non-nil plugin.
func TestHandleCreatePaneSetupKey_Routing(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "claude-code.toml")
	content := `[plugin]
name = "claude-code"
display_name = "Claude Code"
category = "ai"

[command]
cmd = "true"
prompts_cwd = true

[[command.toggles]]
name = "skip"
label = "Skip permissions"
args_when_on = ["--dangerously-skip-permissions"]
default = false
`
	if err := os.WriteFile(tomlPath, []byte(content), 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	// Helper: build a Model that's already inside the setup dialog with the
	// browser pre-loaded with three entries (so cursor moves are observable).
	freshModel := func() Model {
		return Model{
			pluginRegistry:   r,
			selectedPlugin:   "claude-code",
			dialog:           dialogCreatePaneSetup,
			cwdBrowseDir:     "/fake/root",
			cwdBrowseEntries: []string{"..", "alpha", "beta"},
			toggleStates:     []bool{false},
			setupFieldCursor: 0, // CWD field
		}
	}

	t.Run("tab advances field cursor across CWD → toggle → Continue", func(t *testing.T) {
		m := freshModel()
		// CWD → toggle
		next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(Model)
		kind, _ := m.setupFieldKind(r.Get("claude-code"), m.setupFieldCursor)
		if kind != "toggle" {
			t.Errorf("after tab from cwd: kind = %q, want toggle", kind)
		}
		// toggle → Continue
		next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(Model)
		kind, _ = m.setupFieldKind(r.Get("claude-code"), m.setupFieldCursor)
		if kind != "continue" {
			t.Errorf("after tab from toggle: kind = %q, want continue", kind)
		}
		// Continue → wrap to CWD
		next, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(Model)
		kind, _ = m.setupFieldKind(r.Get("claude-code"), m.setupFieldCursor)
		if kind != "cwd" {
			t.Errorf("after tab from continue: kind = %q, want cwd (wrap)", kind)
		}
	})

	t.Run("shift+tab cycles backwards", func(t *testing.T) {
		m := freshModel()
		next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		m = next.(Model)
		kind, _ := m.setupFieldKind(r.Get("claude-code"), m.setupFieldCursor)
		if kind != "continue" {
			t.Errorf("after shift+tab from cwd: kind = %q, want continue (wrap-back)", kind)
		}
	})

	t.Run("esc with no instance form returns to plugin picker step 1", func(t *testing.T) {
		m := freshModel()
		next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = next.(Model)
		if m.dialog != dialogCreatePane {
			t.Errorf("after esc: dialog = %v, want dialogCreatePane", m.dialog)
		}
		if m.createPaneStep != 1 {
			t.Errorf("after esc: createPaneStep = %d, want 1", m.createPaneStep)
		}
	})

	t.Run("space on focused toggle flips it", func(t *testing.T) {
		m := freshModel()
		// Move cursor to the toggle field.
		m.setupFieldCursor = 1
		// In Bubble Tea v2, KeyPressMsg.String() returns the textual key
		// name. For Space the canonical name is "space" — passing Code = ' '
		// + Text = " " yields that name reliably across versions.
		spaceKey := tea.KeyPressMsg{Code: ' ', Text: " "}
		next, _ := m.handleCreatePaneSetupKey(spaceKey)
		m = next.(Model)
		if !m.toggleStates[0] {
			t.Errorf("space on toggle should flip false → true (key.String()=%q)", spaceKey.String())
		}
		// Toggle again.
		next, _ = m.handleCreatePaneSetupKey(spaceKey)
		m = next.(Model)
		if m.toggleStates[0] {
			t.Error("second space should flip true → false")
		}
	})

	t.Run("enter on Continue submits", func(t *testing.T) {
		m := freshModel()
		m.setupFieldCursor = 2 // Continue button
		next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		if m.dialog != dialogCreatePane {
			t.Errorf("after enter on continue: dialog = %v, want dialogCreatePane", m.dialog)
		}
		if m.createPaneStep != 3 {
			t.Errorf("after enter on continue: createPaneStep = %d, want 3", m.createPaneStep)
		}
	})

	t.Run("nil registry plugin lookup unwinds gracefully", func(t *testing.T) {
		m := Model{
			pluginRegistry: r,
			selectedPlugin: "no-such-plugin",
			dialog:         dialogCreatePaneSetup,
		}
		next, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = next.(Model)
		if m.dialog != dialogCreatePane {
			t.Errorf("missing plugin: dialog = %v, want dialogCreatePane", m.dialog)
		}
		if m.createPaneStep != 1 {
			t.Errorf("missing plugin: createPaneStep = %d, want 1", m.createPaneStep)
		}
	})
}

// TestLoadBrowseDirAndSelect_PositionsCursorOnChild guards the Q12 polish fix:
// going up to the parent should highlight the directory we just exited.
func TestLoadBrowseDirAndSelect_PositionsCursorOnChild(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	m := &Model{}
	if err := m.loadBrowseDirAndSelect(root, "beta"); err != nil {
		t.Fatalf("loadBrowseDirAndSelect: %v", err)
	}
	// Listing: ["..", "alpha", "beta", "gamma"]; "beta" is index 2.
	if m.cwdBrowseCursor != 2 {
		t.Errorf("cursor = %d, want 2 (beta)", m.cwdBrowseCursor)
	}

	// Unknown name leaves cursor at 0.
	if err := m.loadBrowseDirAndSelect(root, "no-such-dir"); err != nil {
		t.Fatalf("loadBrowseDirAndSelect: %v", err)
	}
	if m.cwdBrowseCursor != 0 {
		t.Errorf("cursor = %d, want 0 for unknown selectName", m.cwdBrowseCursor)
	}
}
