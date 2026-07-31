package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/kubediscover"
	"github.com/artyomsv/quil/internal/plugin"
)

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

	t.Run("cwd only — opens setup and asks the daemon for the home fallback", func(t *testing.T) {
		m, fake, _ := overlayTestModel(t, "") // no pane CWD: that candidate is skipped
		cmd := m.enterSetupOrSplit(pluginCWD)
		if cmd == nil {
			t.Fatal("expected non-nil cmd (ClearScreen + browse request) when opening dialog")
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
		runCmd(cmd)
		// The home fallback is the literal "~" for the DAEMON to expand.
		// os.UserHomeDir here would name this machine's home, which against a
		// remote host is a directory that does not exist there.
		if got := browseReqPaths(t, fake); len(got) != 1 || got[0] != "~" {
			t.Errorf("browse requests = %v, want exactly [~]", got)
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

	// The chain is ordered lastSelectedCWD -> active pane CWD -> "~", and only
	// the head of it is asked about up front. Its later links are exercised in
	// browse_client_test.go, where the failing responses that advance them can
	// be delivered.
	t.Run("cwd — lastSelectedCWD is asked about first", func(t *testing.T) {
		m, fake, _ := overlayTestModel(t, "/pane/cwd")
		m.lastSelectedCWD = "/remembered"
		runCmd(m.enterSetupOrSplit(pluginCWD))
		if got := browseReqPaths(t, fake); len(got) != 1 || got[0] != "/remembered" {
			t.Errorf("browse requests = %v, want exactly [/remembered]", got)
		}
	})

	t.Run("cwd — active pane CWD is asked about when nothing is remembered", func(t *testing.T) {
		m, fake, _ := overlayTestModel(t, "/pane/cwd")
		runCmd(m.enterSetupOrSplit(pluginCWD))
		if got := browseReqPaths(t, fake); len(got) != 1 || got[0] != "/pane/cwd" {
			t.Errorf("browse requests = %v, want exactly [/pane/cwd]", got)
		}
	})
}

// The listing itself is no longer read here — it arrives from the daemon, and
// applyBrowseDir's fill (sort, file filtering, the ".." row) is covered in
// browse_client_test.go.

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

// TestSanitizePastedPath_StripsControlBytes guards the S1 security fix:
// clipboard payloads with embedded OSC/CSI escape sequences must NOT survive
// past sanitizePastedPath, otherwise a daemon error message that quotes the
// input could inject terminal escapes into the rendered browse error.
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

	// Descend and up no longer touch the local filesystem — they emit a request
	// and the listing arrives from the daemon. What they SEND is the thing worth
	// pinning now, and that lives in browse_client_test.go:
	// TestSetupCWDKey_DescendSendsChildNotAJoin,
	// TestSetupCWDKey_UpSendsDaemonReportedParent and
	// TestSetupCWDKey_UpCarriesTheExitedLeafAsSelectName.

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

// The cursor-positioning half of the Q12 polish fix now lives on the daemon's
// listing — see TestApplyBrowseListing_SelectName in browse_client_test.go.

func TestEnterSetupOrSplit_GitDiscover_PopulatesCandidates(t *testing.T) {
	// No .git directory needed on disk any more — discovery is asked of the
	// daemon (RD-021), and the answer below is what a real scan would have
	// found. Only the base path this test's fake daemon is asked about, and
	// echoes back, has to be real.
	root := t.TempDir()

	pane := NewPaneModel("pane-1", 1024)
	pane.CWD = root
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	fake := &fakeSender{}
	m := &Model{tabs: []*TabModel{tab}, activeTab: 0, client: fake}
	p := &plugin.PanePlugin{
		Name: "lazygit",
		Command: plugin.CommandConfig{
			Cmd:        "lazygit",
			PromptsCWD: true,
			Discover:   "git",
		},
	}
	runCmd(m.enterSetupOrSplit(p))
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: root, Repos: []string{root}}, m.repoScan.gen))

	if len(m.repoCandidates) != 1 || m.repoCandidates[0] != root {
		t.Fatalf("repoCandidates = %v, want [%q]", m.repoCandidates, root)
	}
	if m.cwdBrowseDir != root {
		t.Errorf("cwdBrowseDir = %q, want first candidate pre-selected %q", m.cwdBrowseDir, root)
	}
	if m.dialog != dialogCreatePaneSetup {
		t.Errorf("dialog = %v, want dialogCreatePaneSetup", m.dialog)
	}
}

func TestEnterSetupOrSplit_GitDiscover_NoRepo_FallsBackToBrowser(t *testing.T) {
	plain := t.TempDir()
	pane := NewPaneModel("pane-1", 1024)
	pane.CWD = plain
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	fake := &fakeSender{}
	m := &Model{tabs: []*TabModel{tab}, activeTab: 0, client: fake}
	p := &plugin.PanePlugin{
		Name: "lazygit",
		Command: plugin.CommandConfig{
			Cmd:        "lazygit",
			PromptsCWD: true,
			Discover:   "git",
		},
	}
	runCmd(m.enterSetupOrSplit(p))
	// No error and no repos is a real finding — "no repo here" — which falls
	// back to the same recent/browser chain a non-git PromptsCWD plugin uses.
	// That fallback used to run synchronously inside enterSetupOrSplit; it now
	// runs in applyGitReposPickList once this response lands.
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: plain}, m.repoScan.gen))

	if len(m.repoCandidates) != 0 {
		t.Fatalf("repoCandidates = %v, want empty", m.repoCandidates)
	}
	// The listing now arrives from the daemon, so the fallback shows up as the
	// request it asks about rather than as a filled cwdBrowseDir.
	if got := browseReqPaths(t, fake); len(got) != 1 || got[0] != plain {
		t.Errorf("browse requests = %v, want [%q] (the pane CWD as browser fallback)", got, plain)
	}
}

func TestEnterSetupOrSplit_GitDiscover_ClearsStaleCandidates(t *testing.T) {
	m := &Model{repoCandidates: []string{"/stale"}}
	m.enterSetupOrSplit(&plugin.PanePlugin{Name: "terminal", Command: plugin.CommandConfig{Cmd: "sh"}})
	if m.repoCandidates != nil {
		t.Errorf("repoCandidates = %v, want nil", m.repoCandidates)
	}
}

// registryWithLazygit returns a plugin registry containing a minimal
// lazygit plugin with PromptsCWD+Discover set and Available forced true.
// Registry has NO Register method — the established pattern (see the
// raw-keys test ~line 440) is temp TOML + LoadFromDir, then flip the
// exported Available field via Get.
func registryWithLazygit(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	content := `[plugin]
name = "lazygit"

[command]
cmd = "lazygit"
prompts_cwd = true
discover = "git"
`
	if err := os.WriteFile(filepath.Join(dir, "lazygit.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("lazygit").Available = true
	return r
}

// repoPickModel builds a Model sitting in the setup dialog with a candidate
// list, mirroring what enterSetupOrSplit produces for discover="git".
func repoPickModel(t *testing.T, candidates []string) Model {
	t.Helper()
	return Model{
		dialog:         dialogCreatePaneSetup,
		repoCandidates: candidates,
		cwdBrowseDir:   candidates[0],
		pluginRegistry: registryWithLazygit(t),
		selectedPlugin: "lazygit",
	}
}

func TestSetupRepoKey_DownMovesAndSelects(t *testing.T) {
	m := repoPickModel(t, []string{"/repo-a", "/repo-b"})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	got := out.(Model)
	if got.cwdBrowseCursor != 1 {
		t.Errorf("cursor = %d, want 1", got.cwdBrowseCursor)
	}
	if got.cwdBrowseDir != "/repo-b" {
		t.Errorf("cwdBrowseDir = %q, want /repo-b", got.cwdBrowseDir)
	}
}

func TestSetupRepoKey_BrowseRowFallsBackToBrowser(t *testing.T) {
	m := repoPickModel(t, []string{"/repo-a"})
	m.cwdBrowseCursor = 1 // the "Browse…" row (index == len(candidates))
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.repoCandidates != nil {
		t.Errorf("repoCandidates = %v, want nil after Browse…", got.repoCandidates)
	}
}

func TestSetupRepoKey_EnterOnCandidateSubmits(t *testing.T) {
	m := repoPickModel(t, []string{"/repo-a", "/repo-b"})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.selectedCWD != "/repo-a" {
		t.Errorf("selectedCWD = %q, want /repo-a (submitted candidate)", got.selectedCWD)
	}
	if got.dialog != dialogCreatePane || got.createPaneStep != 3 {
		t.Errorf("dialog/step = %v/%d, want dialogCreatePane/3 (split selection)", got.dialog, got.createPaneStep)
	}
}

func TestSetupRepoKey_UpAtTopStays(t *testing.T) {
	m := repoPickModel(t, []string{"/repo-a", "/repo-b"})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyUp})
	got := out.(Model)
	if got.cwdBrowseCursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped at top)", got.cwdBrowseCursor)
	}
	if got.cwdBrowseDir != "/repo-a" {
		t.Errorf("cwdBrowseDir = %q, want /repo-a (unchanged)", got.cwdBrowseDir)
	}
}

func TestSetupRepoKey_DownAtBrowseRowStays(t *testing.T) {
	m := repoPickModel(t, []string{"/repo-a", "/repo-b"})
	m.cwdBrowseCursor = 2 // the "Browse…" row (index == len(candidates))
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	got := out.(Model)
	if got.cwdBrowseCursor != 2 {
		t.Errorf("cursor = %d, want 2 (clamped at Browse… row)", got.cwdBrowseCursor)
	}
}

// --- Recent-locations quick pick ---

// registryWithAICWD returns a registry with a minimal prompts_cwd plugin (no
// discover) — the shape that drops into the recent-locations pick list.
func registryWithAICWD(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	content := `[plugin]
name = "ai"

[command]
cmd = "ai"
prompts_cwd = true
`
	if err := os.WriteFile(filepath.Join(dir, "ai.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("ai").Available = true
	return r
}

// recentPickModel builds a Model sitting in the setup dialog in recent-pick
// mode, mirroring what enterSetupOrSplit produces when no git repos exist.
func recentPickModel(t *testing.T, candidates []string) Model {
	t.Helper()
	return Model{
		dialog:           dialogCreatePaneSetup,
		recentCandidates: candidates,
		cwdBrowseDir:     candidates[0],
		pluginRegistry:   registryWithAICWD(t),
		selectedPlugin:   "ai",
	}
}

func TestEnterSetup_RecentPickWhenNoRepos(t *testing.T) {
	dir := t.TempDir()
	m := &Model{recentCWDs: []string{dir, filepath.Join(dir, "gone")}}
	p := &plugin.PanePlugin{Name: "ai", Command: plugin.CommandConfig{PromptsCWD: true}}
	m.enterSetupOrSplit(p)
	// Only the existing dir survives the os.Stat filter.
	if !reflect.DeepEqual(m.recentCandidates, []string{dir}) {
		t.Errorf("recentCandidates = %v, want [%q]", m.recentCandidates, dir)
	}
	if m.cwdBrowseDir != dir {
		t.Errorf("cwdBrowseDir = %q, want first candidate %q", m.cwdBrowseDir, dir)
	}
	if m.dialog != dialogCreatePaneSetup {
		t.Errorf("dialog = %v, want dialogCreatePaneSetup", m.dialog)
	}
}

func TestEnterSetup_RecentAllStaleFallsToBrowser(t *testing.T) {
	base := t.TempDir()
	fake := &fakeSender{}
	m := &Model{
		recentCWDs: []string{filepath.Join(base, "gone1"), filepath.Join(base, "gone2")},
		client:     fake,
	}
	p := &plugin.PanePlugin{Name: "ai", Command: plugin.CommandConfig{PromptsCWD: true}}
	runCmd(m.enterSetupOrSplit(p))
	if len(m.recentCandidates) != 0 {
		t.Errorf("recentCandidates = %v, want empty (all stale)", m.recentCandidates)
	}
	// No tab, nothing remembered, so the chain is down to its "~" tail — but a
	// request going out at all is what proves the browser took over.
	if got := browseReqPaths(t, fake); len(got) != 1 || got[0] != "~" {
		t.Errorf("browse requests = %v, want [~] (directory-browser fallback)", got)
	}
}

func TestEnterSetup_GitReposWinOverRecent(t *testing.T) {
	root := t.TempDir()
	pane := NewPaneModel("pane-1", 1024)
	pane.CWD = root
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	fake := &fakeSender{}
	m := &Model{tabs: []*TabModel{tab}, activeTab: 0, recentCWDs: []string{t.TempDir()}, client: fake}
	p := &plugin.PanePlugin{Name: "lazygit", Command: plugin.CommandConfig{Cmd: "lazygit", PromptsCWD: true, Discover: "git"}}
	runCmd(m.enterSetupOrSplit(p))
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: root, Repos: []string{root}}, m.repoScan.gen))

	if len(m.repoCandidates) != 1 || m.repoCandidates[0] != root {
		t.Fatalf("repoCandidates = %v, want [%q]", m.repoCandidates, root)
	}
	if m.recentCandidates != nil {
		t.Errorf("recentCandidates = %v, want nil (git repos take priority)", m.recentCandidates)
	}
}

func TestSetupRecentKey_EnterSubmitsCandidate(t *testing.T) {
	dir := t.TempDir()
	m := recentPickModel(t, []string{dir})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.selectedCWD != dir {
		t.Errorf("selectedCWD = %q, want %q (submitted recent location)", got.selectedCWD, dir)
	}
}

func TestSetupRecentKey_BrowseRowFallsBackToBrowser(t *testing.T) {
	m := recentPickModel(t, []string{t.TempDir()})
	m.cwdBrowseCursor = 1 // the "Browse…" row (index == len(candidates))
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.recentCandidates != nil {
		t.Errorf("recentCandidates = %v, want nil after Browse…", got.recentCandidates)
	}
}

// TestHandleCreatePaneSplit_PersistsRecentCWD covers the integration point:
// selecting a placement at step 3 must push the chosen CWD into recentCWDs and
// persist it to disk. QUIL_HOME is redirected to a tempdir so the production
// ~/.quil is never touched.
func TestHandleCreatePaneSplit_PersistsRecentCWD(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	dir := t.TempDir()
	m := Model{selectedCWD: dir}
	out, _ := m.handleCreatePaneSplit()
	got := out.(Model)

	want := filepath.Clean(dir)
	if len(got.recentCWDs) != 1 || got.recentCWDs[0] != want {
		t.Errorf("in-memory recentCWDs = %v, want [%q]", got.recentCWDs, want)
	}
	if disk := LoadRecentCWDs(config.RecentCWDsPath()); len(disk) != 1 || disk[0] != want {
		t.Errorf("persisted recentCWDs = %v, want [%q]", disk, want)
	}
}

func TestHandleCreatePaneSplit_BlankCWD_NoPersist(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	m := Model{selectedCWD: ""}
	out, _ := m.handleCreatePaneSplit()
	got := out.(Model)
	if len(got.recentCWDs) != 0 {
		t.Errorf("recentCWDs = %v, want empty for blank cwd", got.recentCWDs)
	}
	if disk := LoadRecentCWDs(config.RecentCWDsPath()); len(disk) != 0 {
		t.Errorf("persisted %v, want nothing written for blank cwd", disk)
	}
}

// TestRenderSetup_RecentPick_NoDuplicatePathLine guards issue 1: in pick mode
// the highlighted row IS the selected path, so there must be no separate
// "current path" line duplicating it above the list.
func TestRenderSetup_RecentPick_NoDuplicatePathLine(t *testing.T) {
	const short = "/tmp/proj" // short enough that leftTruncPath won't shorten it
	m := recentPickModel(t, []string{short})
	m.setupFieldCursor = 0 // focus the CWD field
	out := m.renderCreatePaneSetupDialog()
	if n := strings.Count(out, short); n != 1 {
		t.Errorf("path %q appears %d times in pick-mode render, want exactly 1 (the highlighted row)", short, n)
	}
}

// TestRenderSetup_BrowserHint_FitsTextArea guards issue 2: no rendered line may
// exceed the box text area, or it wraps onto a second line. Uses a narrow width
// that reproduced the original hint wrap.
func TestRenderSetup_BrowserHint_FitsTextArea(t *testing.T) {
	entries := []string{".."}
	for i := 0; i < 15; i++ {
		entries = append(entries, fmt.Sprintf("dir%02d", i))
	}
	m := Model{
		dialog:           dialogCreatePaneSetup,
		pluginRegistry:   registryWithAICWD(t),
		selectedPlugin:   "ai",
		cwdBrowseDir:     `E:\Projects\Stukans\monorepo`,
		cwdBrowseEntries: entries,
		setupFieldCursor: 0,
		width:            64, // narrow box → text area 58; the old 65-wide hint wrapped here
	}
	out := m.renderCreatePaneSetupDialog()
	textArea := m.setupTextWidth()
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > textArea {
			t.Errorf("line %q width %d exceeds text area %d (would wrap)", line, w, textArea)
		}
	}
}

// TestSetupBoxChrome_MatchesLipglossWrapLimit pins setupBoxChrome to what
// lipgloss actually does, rather than to a reading of its docs. Every content
// budget in the setup dialog derives from this constant, so if a lipgloss
// upgrade moves the border inside or outside Style.Width, this fails here
// instead of silently re-wrapping the session list.
//
// Two-sided on purpose: too small and rows wrap; too large and every row is
// truncated shorter than it needs to be, which is invisible in a screenshot.
func TestSetupBoxChrome_MatchesLipglossWrapLimit(t *testing.T) {
	const boxW = 70
	// A line of exactly n cells ending in a separate word, so reflow has a
	// break point — an unbreakable token wraps under different rules.
	line := func(n int) string { return strings.Repeat("a", n-5) + " word" }
	lines := func(s string) int {
		return len(strings.Split(dialogBorder.Width(boxW).Render(s), "\n"))
	}

	base := lines("x")
	limit := boxW - setupBoxChrome

	if got := lines(line(limit)); got != base {
		t.Errorf("a %d-cell line wrapped inside a %d-wide box (%d lines, want %d): setupBoxChrome=%d is too small",
			limit, boxW, got, base, setupBoxChrome)
	}
	if got := lines(line(limit + 1)); got != base+1 {
		t.Errorf("a %d-cell line did not wrap inside a %d-wide box (%d lines, want %d): setupBoxChrome=%d is larger than needed, so rows truncate early",
			limit+1, boxW, got, base+1, setupBoxChrome)
	}
}

// TestClaudeCodeToggleLabelsFitFloor guards Part 1: the shortened toggle
// labels must fit the floor-70 setup box so they never wrap mid-word.
func TestClaudeCodeToggleLabelsFitFloor(t *testing.T) {
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("ensure defaults: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	p := r.Get("claude-code")
	if p == nil {
		t.Fatal("claude-code plugin not found")
	}
	// Floor-70 box: 6 cells row chrome ("> " + "[x] ") + setupBoxChrome (6) =>
	// 58 usable label cells.
	const maxLabel = 70 - 6 - setupBoxChrome
	for _, tg := range p.Command.Toggles {
		if w := lipgloss.Width(tg.Label); w > maxLabel {
			t.Errorf("label %q width %d exceeds %d (would wrap in floor-70 box)", tg.Label, w, maxLabel)
		}
	}
}

// registryWithK9s mirrors registryWithLazygit: temp TOML + LoadFromDir, then
// flip Available. k9s has prompts_cwd=false and discover=kube.
func registryWithK9s(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	content := `[plugin]
name = "k9s"

[command]
cmd = "k9s"
prompts_cwd = false
discover = "kube"
`
	if err := os.WriteFile(filepath.Join(dir, "k9s.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write test toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	r.Get("k9s").Available = true
	return r
}

// kubePickModel builds a Model sitting in the setup dialog with a kube field.
func kubePickModel(t *testing.T, ctxs []kubediscover.Context) Model {
	t.Helper()
	return Model{
		dialog:         dialogCreatePaneSetup,
		kubeContexts:   ctxs,
		kubeCursor:     0,
		pluginRegistry: registryWithK9s(t),
		selectedPlugin: "k9s",
	}
}

func TestSetupFieldKind_KubeFieldFirst(t *testing.T) {
	t.Parallel()
	m := Model{}
	p := &plugin.PanePlugin{Command: plugin.CommandConfig{Discover: "kube"}}
	if kind, _ := m.setupFieldKind(p, 0); kind != "kube" {
		t.Errorf("field 0 kind = %q, want kube", kind)
	}
	// Only field besides Continue → count is 2.
	if got := m.setupFieldCount(p); got != 2 {
		t.Errorf("setupFieldCount = %d, want 2 (kube + continue)", got)
	}
}

func TestSetupKubeKey_DownMoves(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod", Current: true}, {Name: "staging"}})
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	got := out.(Model)
	if got.kubeCursor != 1 {
		t.Errorf("kubeCursor = %d, want 1", got.kubeCursor)
	}
}

func TestSubmitSetup_KubeContext_InjectsContextArg(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}, {Name: "staging"}})
	p := m.pluginRegistry.Get("k9s")
	m.kubeCursor = 2 // row 0 = Default, row 1 = prod, row 2 = staging
	out, _ := m.submitSetupDialog(p)
	got := out.(Model)
	want := []string{"--context", "staging"}
	if len(got.selectedInstanceArgs) != 2 || got.selectedInstanceArgs[0] != want[0] || got.selectedInstanceArgs[1] != want[1] {
		t.Errorf("selectedInstanceArgs = %v, want %v", got.selectedInstanceArgs, want)
	}
}

func TestSubmitSetup_KubeDefaultRow_NoContextArg(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}})
	p := m.pluginRegistry.Get("k9s")
	m.kubeCursor = 0 // Default context
	out, _ := m.submitSetupDialog(p)
	got := out.(Model)
	for _, a := range got.selectedInstanceArgs {
		if a == "--context" {
			t.Errorf("Default row must not inject --context, got %v", got.selectedInstanceArgs)
		}
	}
}

func TestSetupKubeKey_UpClampAtTop(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}, {Name: "staging"}})
	m.kubeCursor = 0
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := out.(Model); got.kubeCursor != 0 {
		t.Errorf("kubeCursor = %d, want 0 (clamped at top)", got.kubeCursor)
	}
}

func TestSetupKubeKey_DownClampAtBottom(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}, {Name: "staging"}})
	m.kubeCursor = 2 // rows = 2 contexts + Default = 3; bottom index = 2
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := out.(Model); got.kubeCursor != 2 {
		t.Errorf("kubeCursor = %d, want 2 (clamped at bottom)", got.kubeCursor)
	}
}

func TestSetupKubeKey_EnterSubmitsAndInjects(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}})
	m.kubeCursor = 1 // prod
	out, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.dialog != dialogCreatePane || got.createPaneStep != 3 {
		t.Errorf("dialog/step = %v/%d, want dialogCreatePane/3 (advanced via enter dispatch)", got.dialog, got.createPaneStep)
	}
	if len(got.selectedInstanceArgs) != 2 || got.selectedInstanceArgs[0] != "--context" || got.selectedInstanceArgs[1] != "prod" {
		t.Errorf("selectedInstanceArgs = %v, want [--context prod]", got.selectedInstanceArgs)
	}
}

// TestEnterSetupOrSplit_Kube_RequestsFromDaemonAndCaps mirrors
// TestEnterSetupOrSplit_GitDiscover_PopulatesCandidates: entering the setup
// dialog on a discover="kube" plugin now asks the daemon rather than reading
// the local kubeconfig synchronously (RD-022) — kubediscover run in this
// process would parse the machine drawing the UI, the wrong one whenever the
// daemon is remote. The cap this test used to exercise via a real KUBECONFIG
// file is enforced again on receipt; see
// TestApplyKubeContexts_CapsAnOversizedResponse for that seam pinned in
// isolation.
func TestEnterSetupOrSplit_Kube_RequestsFromDaemonAndCaps(t *testing.T) {
	fake := &fakeSender{}
	m := &Model{client: fake, pluginRegistry: registryWithK9s(t)}
	runCmd(m.enterSetupOrSplit(m.pluginRegistry.Get("k9s")))

	var asked bool
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgKubeCtxReq {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("entering setup for a discover=kube plugin sent no %s (sent: %v)", ipc.MsgKubeCtxReq, debugSentTypes(fake))
	}
	if m.kubeScan.phase != kubeScanning {
		t.Errorf("phase = %v, want kubeScanning", m.kubeScan.phase)
	}

	big := make([]ipc.KubeContextInfo, maxKubeContexts+5)
	for i := range big {
		big[i] = ipc.KubeContextInfo{Name: fmt.Sprintf("ctx-%d", i)}
	}
	m.applyKubeContexts(ipc.KubeCtxRespPayload{Contexts: big}, m.kubeScan.gen)

	if len(m.kubeContexts) != maxKubeContexts {
		t.Errorf("kubeContexts = %d, want capped to %d", len(m.kubeContexts), maxKubeContexts)
	}
}

// TestEnterSetupOrSplit_GitDiscover_CapsCandidates guards the maxRepoCandidates
// bound: the pick list has no scroll machinery, so discovery must never hand
// the dialog more rows than fit the box.
func TestEnterSetupOrSplit_GitDiscover_CapsCandidates(t *testing.T) {
	base := t.TempDir()
	pane := NewPaneModel("pane-1", 1024)
	pane.CWD = base
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	fake := &fakeSender{}
	m := &Model{tabs: []*TabModel{tab}, activeTab: 0, client: fake}
	p := &plugin.PanePlugin{
		Name: "lazygit",
		Command: plugin.CommandConfig{
			Cmd:        "lazygit",
			PromptsCWD: true,
			Discover:   "git",
		},
	}
	runCmd(m.enterSetupOrSplit(p))

	// The daemon reports 12 candidates; the client-side cap must still trim
	// to maxRepoCandidates — the pick list has no scroll machinery.
	repos := make([]string, 12)
	for i := range repos {
		repos[i] = fmt.Sprintf("%s/repo-%02d", base, i)
	}
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: base, Repos: repos}, m.repoScan.gen))

	if len(m.repoCandidates) != maxRepoCandidates {
		t.Fatalf("len(repoCandidates) = %d, want %d (capped)", len(m.repoCandidates), maxRepoCandidates)
	}
}

// TestSetupDialogWidth_FloorGrowthAndCap exercises the three branches of
// setupDialogWidth: the floor (no/short labels), label-driven growth (the box
// widens to fit the longest toggle row), and the terminal-width cap (the box
// never exceeds m.width). The growth/cap expectations are derived from the same
// label length the registry is built with, so the test stays correct if the
// helper's overhead constants change meaning but not value.
func TestSetupDialogWidth_FloorGrowthAndCap(t *testing.T) {
	// rowOverhead mirrors setupDialogWidth: "> " prefix (2) + "[x] " box (4) +
	// setupBoxChrome (6: 2 border + Padding(1,2)'s 4) = 12 cells on top of the
	// label width.
	const rowOverhead = 12

	// A label long enough to push the box well past the floor of 70.
	longLabel := strings.Repeat("x", 80)       // 80 cells wide (ASCII → 1 cell each)
	grownWidth := rowOverhead + len(longLabel) // 92

	dir := t.TempDir()
	// Plugin "wide" has a long label; "narrow" has only a short label.
	wideTOML := fmt.Sprintf(`[plugin]
name = "wide"
display_name = "Wide"
category = "ai"

[command]
cmd = "true"

[[command.toggles]]
name = "long"
label = %q
args_when_on = ["--long"]
default = false
`, longLabel)
	narrowTOML := `[plugin]
name = "narrow"
display_name = "Narrow"
category = "ai"

[command]
cmd = "true"

[[command.toggles]]
name = "short"
label = "Short"
args_when_on = ["--short"]
default = false
`
	if err := os.WriteFile(filepath.Join(dir, "wide.toml"), []byte(wideTOML), 0644); err != nil {
		t.Fatalf("write wide.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "narrow.toml"), []byte(narrowTOML), 0644); err != nil {
		t.Fatalf("write narrow.toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	tests := []struct {
		name     string
		width    int
		selected string
		want     int
	}{
		{"unknown plugin falls back to floor", 200, "no-such-plugin", 70},
		{"short label stays at floor", 200, "narrow", 70},
		{"long label grows past floor", 200, "wide", grownWidth},
		{"grown width capped to terminal", 80, "wide", 80 - 2},
		{"unset width skips cap", 0, "wide", grownWidth},
		{"floor capped to tiny terminal", 40, "narrow", 40 - 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				pluginRegistry: r,
				selectedPlugin: tt.selected,
				width:          tt.width,
			}
			if got := m.setupDialogWidth(); got != tt.want {
				t.Errorf("setupDialogWidth() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- Blurred-field selection markers -----------------------------------------
//
// Tabbing off a pick-list field used to erase every trace of what was chosen:
// the highlight was gated on `focused`, so the CWD field read as "nothing
// selected" while the rest of the pane was being configured. These guard the
// idle marker that stands in for the cursor caret on the committed row.
// Assertions run on ANSI-stripped output and key off the plain-text row prefix
// — bold survives no color profile, but the marker does.

func TestRenderSetup_PickBlurred_MarksCommittedRow(t *testing.T) {
	const picked, other = "/tmp/alpha", "/tmp/beta"
	m := recentPickModel(t, []string{picked, other})
	m.setupFieldCursor = 1 // Continue — the CWD field is blurred

	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, setupRowIdleMark+picked) {
		t.Errorf("blurred pick list must mark the committed row %q with %q\n%s", picked, setupRowIdleMark, out)
	}
	if strings.Contains(out, setupRowIdleMark+other) {
		t.Errorf("only the committed row may carry the idle mark, but %q has it too\n%s", other, out)
	}
	if strings.Contains(out, "  > "+picked) {
		t.Errorf("blurred field must not draw the cursor caret\n%s", out)
	}
}

// When the cursor already sits on the committed row, the caret wins and no
// second indicator is drawn — one row, one marker.
func TestRenderSetup_PickFocused_CursorRowTakesCaretNotIdleMark(t *testing.T) {
	const picked = "/tmp/alpha"
	m := recentPickModel(t, []string{picked, "/tmp/beta"})
	m.setupFieldCursor = 0 // CWD focused, cursor on row 0 = the committed row

	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "  > "+picked) {
		t.Errorf("focused pick list must keep the cursor caret on the cursor row\n%s", out)
	}
	if strings.Contains(out, setupRowIdleMark) {
		t.Errorf("committed row already has the caret; it must not also be marked\n%s", out)
	}
}

// The blurred case is not the only way to lose the indicator: parking the
// cursor on "Browse…" while the field is still FOCUSED leaves the caret on a
// row that syncSelection never commits, so without a mark the dialog again
// shows nothing for the directory it will actually use.
func TestRenderSetup_PickFocused_CursorOnBrowseMarksCommitted(t *testing.T) {
	const first, second = "/tmp/alpha", "/tmp/beta"
	m := recentPickModel(t, []string{first, second})
	m.cwdBrowseDir = second
	m.cwdBrowseCursor = 2 // the trailing "Browse…" row
	m.setupFieldCursor = 0

	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, setupRowIdleMark+second) {
		t.Errorf("focused field with the cursor on Browse… must still mark %q\n%s", second, out)
	}
	if !strings.Contains(out, "  > Browse…") {
		t.Errorf("the caret must stay on the cursor row\n%s", out)
	}
	if strings.Contains(out, setupRowIdleMark+first) {
		t.Errorf("only the committed row may carry the idle mark\n%s", out)
	}
}

// The cursor can be parked on "Browse…" when the field is blurred; the marker
// must follow cwdBrowseDir (what submitSetupDialog reads), not the cursor.
func TestRenderSetup_PickBlurred_CursorOnBrowseMarksCommitted(t *testing.T) {
	const first, second = "/tmp/alpha", "/tmp/beta"
	m := recentPickModel(t, []string{first, second})
	m.cwdBrowseDir = second
	m.cwdBrowseCursor = 2 // the trailing "Browse…" row
	m.setupFieldCursor = 1

	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, setupRowIdleMark+second) {
		t.Errorf("idle mark must follow cwdBrowseDir %q, not the cursor row\n%s", second, out)
	}
	if strings.Contains(out, setupRowIdleMark+"Browse…") {
		t.Errorf("Browse… is not a committed value and must never carry the idle mark\n%s", out)
	}
}

// Focusing and blurring must not add or remove a row, or every field below the
// CWD list would shift under the cursor on each Tab.
func TestRenderSetup_PickFocusChange_HeightStable(t *testing.T) {
	m := recentPickModel(t, []string{"/tmp/alpha", "/tmp/beta"})

	m.setupFieldCursor = 0
	focused := strings.Count(m.renderCreatePaneSetupDialog(), "\n")
	m.setupFieldCursor = 1
	blurred := strings.Count(m.renderCreatePaneSetupDialog(), "\n")

	if focused != blurred {
		t.Errorf("row count changed on blur: focused=%d blurred=%d", focused, blurred)
	}
}

// Context names now come from a host the user may not control. U+202E
// (RIGHT-TO-LEFT OVERRIDE) is printable, so it passes any control-character
// filter while reversing the text on the list the user picks a cluster from.
func TestRenderSetup_KubeRowsSanitizeRemoteText(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{
		{Name: "prod‮txt.exe", Namespace: "ns\x1b[31m"},
	})
	out := m.renderCreatePaneSetupDialog()
	if strings.ContainsRune(out, '‮') {
		t.Error("rendered output carries U+202E — a remote context name can reverse the pick list")
	}
	if strings.Contains(out, "\x1b[31m") {
		t.Error("rendered output carries a raw CSI sequence from a remote namespace")
	}
}

func TestRenderSetup_KubeBlurred_MarksSelectedRow(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}, {Name: "staging"}})
	m.kubeCursor = 2 // row 0 = Default, row 1 = prod, row 2 = staging
	m.setupFieldCursor = 1

	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, setupRowIdleMark+"staging") {
		t.Errorf("blurred kube field must mark the selected context\n%s", out)
	}
	if strings.Contains(out, setupRowIdleMark+"Default context") {
		t.Errorf("only the selected kube row may carry the idle mark\n%s", out)
	}
}

// The mark stands in for the "  > " caret, so it must cost the same four cells
// — leftTruncPath's budget is setupTextWidth() - setupRowIndent, and a wider
// mark would silently push every marked row one cell past the wrap limit.
// U+25B8 is East-Asian-Ambiguous, so this pins what lipgloss actually measures
// rather than what the glyph "should" be (same exposure as the pre-existing
// "● " current-context marker).
func TestSetupRowIdleMark_MatchesRowIndent(t *testing.T) {
	t.Parallel()
	if got := lipgloss.Width(setupRowIdleMark); got != setupRowIndent {
		t.Errorf("lipgloss.Width(setupRowIdleMark) = %d, want %d", got, setupRowIndent)
	}
}

// Directory-browser mode has no list row to mark — the chosen directory is the
// standalone path line above the listing. It needs the mark for the same reason
// the pick list does: dialogValStyle and dialogNormal are the same declaration
// and dialogSelectedIdle differs from both by SGR bold alone, so on a terminal
// that drops bold the mark carries the entire signal.
func TestRenderSetup_BrowserBlurred_MarksPathLine(t *testing.T) {
	const dir = `E:\Projects\Stukans\monorepo`
	m := Model{
		dialog:           dialogCreatePaneSetup,
		pluginRegistry:   registryWithAICWD(t),
		selectedPlugin:   "ai",
		cwdBrowseDir:     dir,
		cwdBrowseEntries: []string{"..", "cmd", "internal"},
		width:            100,
	}

	m.setupFieldCursor = 1 // blurred
	blurred := m.renderCreatePaneSetupDialog()
	if !strings.Contains(stripANSI(blurred), setupRowIdleMark+dir) {
		t.Errorf("blurred browser mode must mark the path line\n%s", stripANSI(blurred))
	}
	// Assert on the raw frame too: the mark is the fallback, bold is the
	// primary signal, and only unstripped output can prove the style applied.
	if !strings.Contains(blurred, dialogSelectedIdle.Render(dir)) {
		t.Errorf("blurred path line must use dialogSelectedIdle\n%q", blurred)
	}

	m.setupFieldCursor = 0 // focused
	focused := m.renderCreatePaneSetupDialog()
	if strings.Contains(stripANSI(focused), setupRowIdleMark+dir) {
		t.Errorf("focused browser mode must not mark the path line\n%s", stripANSI(focused))
	}
	if !strings.Contains(focused, dialogValStyle.Render(dir)) {
		t.Errorf("focused path line must use dialogValStyle\n%q", focused)
	}
}

// --- Remote-sourced browse text (Phase 3 security) --------------------------
//
// Name, Resolved, and Error in BrowseDirRespPayload come from the daemon,
// which may be remote. These pin that renderCreatePaneSetupDialog never lets
// a raw control byte out of that payload reach the frame — see
// sanitizeRemoteText in remotetext.go for the function under test here.

// A hostile entry name containing a raw ESC byte must not let that byte
// reach the frame. Checked differentially against a same-shaped clean
// listing rather than asserting an absolute zero: whether lipgloss's own
// styling emits SGR (ESC-prefixed) codes at all depends on the color profile
// the test process detects, and the invariant must hold either way — a
// hostile NAME must not ADD an ESC byte, regardless of how many the box's own
// styling contributes.
//
// The ESC-count diff alone is vacuous against a bug that dropped the row
// entirely (fewer bytes total, including the clean row's own chrome, can
// also leave the ESC count unchanged) — so this also asserts the sanitized
// name is still PRESENT, on the ANSI-stripped render so real SGR codes from
// the box's own styling can't hide a missing row inside them.
func TestRenderSetup_HostileEntryName_NoRawESCByte(t *testing.T) {
	clean := Model{
		dialog:           dialogCreatePaneSetup,
		pluginRegistry:   registryWithAICWD(t),
		selectedPlugin:   "ai",
		cwdBrowseDir:     `/home/dev/projects`,
		cwdBrowseEntries: []string{"..", "alpha", "beta"},
		width:            100,
	}
	hostile := clean
	hostileName := "alpha\x1b[31m;rm -rf\x1b[0m"
	hostile.cwdBrowseEntries = []string{"..", hostileName, "beta"}

	cleanOut := clean.renderCreatePaneSetupDialog()
	hostileOut := hostile.renderCreatePaneSetupDialog()

	wantESC := strings.Count(cleanOut, "\x1b")
	if got := strings.Count(hostileOut, "\x1b"); got != wantESC {
		t.Errorf("hostile entry name changed the raw ESC byte count: got %d, want %d (dialog-chrome baseline)\n%s", got, wantESC, hostileOut)
	}

	wantContent := sanitizeRemoteText(hostileName + "/") // "/" appended for a directory row
	if !strings.Contains(stripANSI(hostileOut), wantContent) {
		t.Errorf("sanitized entry name %q missing from render — row may have been dropped rather than cleaned\n%s", wantContent, stripANSI(hostileOut))
	}
}

// A hostile entry name could try to grow the dialog by embedding a raw
// newline instead of an escape. sanitizeRemoteText drops C0 controls
// (\t is the sole exception, mapped to a space) — \n included — so this must
// not change the box's line count. Mirrors
// TestRenderSetup_PickFocusChange_HeightStable.
func TestRenderSetup_HostileEntryName_HeightStable(t *testing.T) {
	base := Model{
		dialog:           dialogCreatePaneSetup,
		pluginRegistry:   registryWithAICWD(t),
		selectedPlugin:   "ai",
		cwdBrowseDir:     `/home/dev/projects`,
		cwdBrowseEntries: []string{"..", "alpha", "beta"},
		width:            100,
	}
	hostile := base
	hostile.cwdBrowseEntries = []string{"..", "alpha\nfake-row\ninjected", "beta"}

	baseLines := strings.Count(base.renderCreatePaneSetupDialog(), "\n")
	hostileLines := strings.Count(hostile.renderCreatePaneSetupDialog(), "\n")
	if baseLines != hostileLines {
		t.Errorf("hostile entry with an embedded newline changed dialog height: got %d lines, want %d", hostileLines, baseLines)
	}
}

// Resolved (cwdBrowseDir) and Error (browse.err) are the other two
// remote-sourced browse fields. Checked by looking for the exact injected
// SGR sequence rather than counting ESC bytes, since these two render sites
// are single-shot (not list rows), so there is no same-shaped clean baseline
// to diff against the way the entry-name test has one.
func TestRenderSetup_HostileResolvedAndError_NoInjectedSGR(t *testing.T) {
	const inject = "\x1b[31m"
	m := Model{
		dialog:         dialogCreatePaneSetup,
		pluginRegistry: registryWithAICWD(t),
		selectedPlugin: "ai",
		cwdBrowseDir:   "/home/dev/" + inject + "evil",
		width:          100,
	}
	m.browse.err = "permission denied: /etc/" + inject + "shadow"
	m.setupFieldCursor = 0 // focus the CWD field so the path line renders

	out := m.renderCreatePaneSetupDialog()
	if strings.Contains(out, inject) {
		t.Errorf("hostile Resolved/Error text leaked a raw SGR escape into the render:\n%q", out)
	}
}

// --- Remote-sourced repository paths (Task C follow-on) ---------------------
//
// GitReposRespPayload.Repos is daemon-supplied and reaches two render sites
// through leftTruncPath: the setup dialog's CWD pick list (repoCandidates)
// and the Alt+G "Open lazygit for which repo?" picker (repoPickCandidates).
// leftTruncPath itself now sanitizes — see its doc comment in dialog.go for
// why sanitizing happens BEFORE truncating rather than after.

// Demonstrates why leftTruncPath must sanitize before it measures the
// truncation budget. Real content sits at the front, garbage padding at the
// end: truncating on the RAW rune count keeps a trailing window that lands
// entirely inside the garbage, and sanitizing THAT afterward would leave
// nothing but the "…" prefix — the one part of the name that was ever real
// is gone. Sanitizing first means maxWidth only ever budgets runes that
// survive to be drawn.
func TestLeftTruncPath_SanitizesBeforeTruncating(t *testing.T) {
	hostile := "realname" + strings.Repeat("\x1b", 50)
	if got := leftTruncPath(hostile, 20); got != "realname" {
		t.Errorf("leftTruncPath(hostile, 20) = %q, want %q (full real content, untruncated once the padding is gone)", got, "realname")
	}
}

// A hostile git-discovered repo path must not let a raw ESC byte reach the
// setup dialog's CWD pick list. Differential against a same-shaped clean
// pick list, same reasoning as TestRenderSetup_HostileEntryName_NoRawESCByte:
// whether lipgloss's own styling emits ESC-prefixed codes depends on the
// color profile the test process detects, so the invariant checked is that a
// hostile candidate does not ADD any.
func TestRenderSetup_HostileRepoCandidate_NoRawESCByte(t *testing.T) {
	clean := Model{
		dialog:         dialogCreatePaneSetup,
		pluginRegistry: registryWithAICWD(t),
		selectedPlugin: "ai",
		repoCandidates: []string{"/home/dev/alpha", "/home/dev/beta"},
		cwdBrowseDir:   "/home/dev/alpha",
		width:          100,
	}
	hostile := clean
	hostileRepo := "/home/dev/alpha\x1b[31m;rm -rf\x1b[0m"
	hostile.repoCandidates = []string{hostileRepo, "/home/dev/beta"}
	// Mirrors applyGitReposPickList, which pre-selects repoCandidates[0] into
	// cwdBrowseDir verbatim — the idle-mark comparison at the render site
	// only lines up (pick[i] == m.cwdBrowseDir) when this is the same raw
	// string, so a test that skipped it would exercise the unmarked branch.
	hostile.cwdBrowseDir = hostileRepo

	cleanOut := clean.renderCreatePaneSetupDialog()
	hostileOut := hostile.renderCreatePaneSetupDialog()

	wantESC := strings.Count(cleanOut, "\x1b")
	if got := strings.Count(hostileOut, "\x1b"); got != wantESC {
		t.Errorf("hostile repo candidate changed the raw ESC byte count: got %d, want %d (dialog-chrome baseline)\n%s", got, wantESC, hostileOut)
	}

	// The ESC-count diff alone would pass vacuously if the row were dropped
	// instead of cleaned — assert the sanitized candidate is still there.
	wantContent := sanitizeRemoteText(hostileRepo)
	if !strings.Contains(stripANSI(hostileOut), wantContent) {
		t.Errorf("sanitized repo candidate %q missing from render — row may have been dropped rather than cleaned\n%s", wantContent, stripANSI(hostileOut))
	}
}

// Same property, for the Alt+G repo picker (renderGitRepoPickDialog), which
// draws repoPickCandidates rather than repoCandidates — a separate field
// populated by a separate caller (resolveLazygitOverlay), so it needs its
// own render-level pin even though both go through leftTruncPath.
func TestRenderGitRepoPick_HostileCandidate_NoRawESCByte(t *testing.T) {
	hostileRepo := "/home/dev/alpha\x1b[31m;rm -rf\x1b[0m"
	clean := Model{repoPickCandidates: []string{"/home/dev/alpha", "/home/dev/beta"}}
	hostile := Model{repoPickCandidates: []string{hostileRepo, "/home/dev/beta"}}

	cleanOut := clean.renderGitRepoPickDialog()
	hostileOut := hostile.renderGitRepoPickDialog()

	wantESC := strings.Count(cleanOut, "\x1b")
	if got := strings.Count(hostileOut, "\x1b"); got != wantESC {
		t.Errorf("hostile repo candidate changed the raw ESC byte count: got %d, want %d (dialog-chrome baseline)\n%s", got, wantESC, hostileOut)
	}

	// The ESC-count diff alone would pass vacuously if the row were dropped
	// instead of cleaned — assert the sanitized candidate is still there.
	wantContent := sanitizeRemoteText(hostileRepo)
	if !strings.Contains(stripANSI(hostileOut), wantContent) {
		t.Errorf("sanitized repo candidate %q missing from render — row may have been dropped rather than cleaned\n%s", wantContent, stripANSI(hostileOut))
	}
}

// Mirror of the CWD case for kube. The kube branch's correctness depends on
// case ORDER inside renderRow's switch, so this is the test that catches a
// reorder — without it, a focused row could silently take the idle mark.
func TestRenderSetup_KubeFocused_NoIdleMark(t *testing.T) {
	m := kubePickModel(t, []kubediscover.Context{{Name: "prod"}, {Name: "staging"}})
	m.kubeCursor = 2
	m.setupFieldCursor = 0 // kube field focused

	out := stripANSI(m.renderCreatePaneSetupDialog())
	if !strings.Contains(out, "  > staging") {
		t.Errorf("focused kube field must draw the caret on the selected row\n%s", out)
	}
	if strings.Contains(out, setupRowIdleMark) {
		t.Errorf("focused kube field must not draw the idle mark\n%s", out)
	}
}
