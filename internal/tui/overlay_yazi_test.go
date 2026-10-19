package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/keymap"
)

// yaziOverlay installs a yazi overlay in tab's slot. cwd is the directory the
// daemon reports for it (the resolved one).
func yaziOverlay(tab *TabModel, cwd string, visible bool) *PaneModel {
	ov := NewPaneModel("pane-yazi", 1024)
	ov.Type = overlayPluginYazi
	ov.CWD = cwd
	tab.overlayPane = ov
	tab.overlayVisible = visible
	return ov
}

// TestToggleYazi_CreatesInThePaneDirectoryWithoutGitDiscovery: yazi opens on
// the active pane's own directory, so the key must create at once rather than
// ask the daemon for git repositories — a folder outside any repository would
// otherwise answer "no git repo here". The directory reaches yazi as the spawn
// CWD alone, so InstanceArgs stay empty.
func TestToggleYazi_CreatesInThePaneDirectoryWithoutGitDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // deliberately NOT a git repository
	m, fake, _ := overlayTestModel(t, dir)

	runCmd(m.handleToggleYazi())

	if got := sentTypesFiltered(fake, ipc.MsgGitReposReq); len(got) != 0 {
		t.Errorf("yazi asked for git repositories: %v", debugSentTypes(fake))
	}
	p := createdPane(t, fake)
	if p.Type != overlayPluginYazi {
		t.Errorf("Type = %q, want yazi", p.Type)
	}
	if p.CWD != dir {
		t.Errorf("CWD = %q, want %q", p.CWD, dir)
	}
	if !p.Overlay {
		t.Error("Overlay must be true")
	}
	if len(p.InstanceArgs) != 0 {
		t.Errorf("InstanceArgs = %v, want empty", p.InstanceArgs)
	}
}

// TestToggleLazygit_StillAsksTheDaemonForRepos: the directory shortcut is
// yazi's alone. The git tools keep resolving repositories on the daemon.
func TestToggleLazygit_StillAsksTheDaemonForRepos(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, t.TempDir())

	runCmd(m.handleToggleLazygit())

	if got := sentTypesFiltered(fake, ipc.MsgGitReposReq); len(got) != 1 {
		t.Errorf("want one git_repos_req, got %v", debugSentTypes(fake))
	}
	if got := sentTypesFiltered(fake, ipc.MsgCreatePane); len(got) != 0 {
		t.Errorf("lazygit created before discovery answered: %v", debugSentTypes(fake))
	}
}

// TestToggleYazi_NoDirectory_FlashesWithoutCreating: a pane that has not
// reported a directory gives yazi nothing to open; the daemon would substitute
// its own. The message names yazi and the reason, not "no git repo".
func TestToggleYazi_NoDirectory_FlashesWithoutCreating(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "")

	runCmd(m.handleToggleYazi())

	if got := sentTypesFiltered(fake, ipc.MsgCreatePane); len(got) != 0 {
		t.Errorf("created with no directory: %v", debugSentTypes(fake))
	}
	if !strings.HasPrefix(m.flashText, "yazi not opened") {
		t.Errorf("flash = %q, want a yazi-specific reason", m.flashText)
	}
}

// TestToggleYazi_SameDirectory_ShowsTheRunningOverlay is the retention rule:
// pressing the key again in the same folder shows the yazi already running,
// with its state, rather than starting a new one.
func TestToggleYazi_SameDirectory_ShowsTheRunningOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fake, tab := overlayTestModel(t, dir)
	yaziOverlay(tab, dir, false)

	runCmd(m.handleToggleYazi())

	if !tab.overlayVisible {
		t.Error("the running yazi overlay must be shown")
	}
	if got := sentTypesFiltered(fake, ipc.MsgCreatePane, ipc.MsgDestroyPane); len(got) != 0 {
		t.Errorf("same folder must not replace the overlay: %v", debugSentTypes(fake))
	}
}

// TestToggleYazi_SymlinkedDirectory_ShowsTheOverlayItOpened: the daemon
// resolves symlinks before spawning, so the overlay reports /private/x while
// the pane still reports /x. The directory this client asked for must match
// too, or every keypress in such a folder restarts yazi and loses its state.
func TestToggleYazi_SymlinkedDirectory_ShowsTheOverlayItOpened(t *testing.T) {
	t.Parallel()
	m, fake, tab := overlayTestModel(t, "/tmp/work")
	yaziOverlay(tab, "/private/tmp/work", false)
	tab.overlayAskedCWD = "/tmp/work"

	runCmd(m.handleToggleYazi())

	if !tab.overlayVisible {
		t.Error("the overlay opened for this folder must be shown")
	}
	if got := sentTypesFiltered(fake, ipc.MsgCreatePane, ipc.MsgDestroyPane); len(got) != 0 {
		t.Errorf("a symlinked folder replaced its own overlay: %v", debugSentTypes(fake))
	}
}

// TestToggleYazi_OtherDirectory_ReplacesTheOverlay: like lazygit with another
// repository, a pane that moved to another folder gets a fresh yazi there.
// Destroy first, then create, in the one Cmd createOverlay sends them in.
func TestToggleYazi_OtherDirectory_ReplacesTheOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fake, tab := overlayTestModel(t, dir)
	yaziOverlay(tab, "/somewhere/else", false)

	runCmd(m.handleToggleYazi())

	got := sentTypesFiltered(fake, ipc.MsgDestroyPane, ipc.MsgCreatePane)
	if len(got) != 2 || got[0] != ipc.MsgDestroyPane || got[1] != ipc.MsgCreatePane {
		t.Fatalf("want destroy then create, got %v", debugSentTypes(fake))
	}
	if p := createdPane(t, fake); p.CWD != dir {
		t.Errorf("new overlay CWD = %q, want %q", p.CWD, dir)
	}
	if tab.overlayAskedCWD != dir {
		t.Errorf("overlayAskedCWD = %q, want %q", tab.overlayAskedCWD, dir)
	}
}

// TestToggleYazi_VisibleYazi_Hides: the tool's own key hides its overlay.
func TestToggleYazi_VisibleYazi_Hides(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "")
	yaziOverlay(tab, "/x", true)

	runCmd(m.handleToggleYazi())

	if tab.overlayVisible {
		t.Error("the yazi key over a visible yazi overlay must hide it")
	}
}

// TestToggleYazi_VisibleLazygit_SwapsTools: one slot per tab, so the yazi key
// over a visible lazygit replaces it — resolved against the NORMAL pane's
// directory, never the outgoing overlay's repository.
func TestToggleYazi_VisibleLazygit_SwapsTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fake, tab := overlayTestModel(t, dir)
	lg := NewPaneModel("pane-lg", 1024)
	lg.Type = overlayPluginLazygit
	lg.CWD = "/some/repo"
	tab.overlayPane, tab.overlayVisible = lg, true

	runCmd(m.handleToggleYazi())

	p := createdPane(t, fake)
	if p.Type != overlayPluginYazi || p.CWD != dir {
		t.Errorf("created %q in %q, want yazi in %q", p.Type, p.CWD, dir)
	}
	if got := sentTypesFiltered(fake, ipc.MsgDestroyPane); len(got) != 1 {
		t.Errorf("the outgoing lazygit was not destroyed: %v", debugSentTypes(fake))
	}
}

// TestToggleYazi_NotInstalled_Flashes: the availability gate keys on yazi's
// own binary, not on the other tools sharing the slot.
func TestToggleYazi_NotInstalled_Flashes(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, t.TempDir())
	m.pluginRegistry.Get(overlayPluginYazi).Available = false

	runCmd(m.handleToggleYazi())

	if got := sentTypesFiltered(fake, ipc.MsgCreatePane); len(got) != 0 {
		t.Errorf("created with yazi missing: %v", debugSentTypes(fake))
	}
	if m.flashText != "yazi not installed" {
		t.Errorf("flash = %q, want %q", m.flashText, "yazi not installed")
	}
}

// TestHandleOverlayKey_YaziKey_SwapsFromHunk: the yazi toggle is on the
// overlay allow-list. Off it, Alt+Shift+F would be typed into hunk.
func TestHandleOverlayKey_YaziKey_SwapsFromHunk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fake, tab := overlayTestModel(t, dir)
	hk := NewPaneModel("pane-hunk", 1024)
	hk.Type = overlayPluginHunk
	tab.overlayPane, tab.overlayVisible = hk, true

	runCmd(m.handleOverlayKey(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 'f'}, tab))

	if got := sentTypesFiltered(fake, ipc.MsgPaneInput); len(got) != 0 {
		t.Errorf("alt+shift+f was typed into hunk: %v", debugSentTypes(fake))
	}
	if p := createdPane(t, fake); p.Type != overlayPluginYazi {
		t.Errorf("created %q, want yazi", p.Type)
	}
}

// TestHandleKey_YaziKey_DispatchesThroughUpdate drives the shipped default
// through Update, so the early-tier case arm is what is under test, not only
// the handler it calls.
func TestHandleKey_YaziKey_DispatchesThroughUpdate(t *testing.T) {
	t.Parallel()
	m, _ := hunkOverlayModel(t)
	tab := m.curTabs()[0]
	yaziOverlay(tab, "", false)
	m.pluginRegistry = registryWithOverlayTools(t)

	updated, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 'f'})
	got := updated.(Model)

	// No pane has a CWD in this fixture, so the key shows the hidden yazi
	// overlay (step 3) — which only happens if the key reached the handler.
	if !got.curTabs()[0].overlayVisible {
		t.Error("alt+shift+f did not reach the yazi overlay handler")
	}
}

// TestYaziDefault_IsAltShiftF_NotAltF pins the default chord. Plain Alt+F is
// readline forward-word (keyToBytes sends ESC-f for Option-as-Meta users), so
// a default on it would take word navigation from every shell pane.
func TestYaziDefault_IsAltShiftF_NotAltF(t *testing.T) {
	t.Parallel()
	km, _ := buildKeymap(config.Default().Keybindings)
	if got, ok := km.MatchTier(keymap.TierEarly, "alt+shift+f"); !ok || got != "pane.toggle_yazi" {
		t.Errorf("alt+shift+f = (%q, %v), want pane.toggle_yazi in the early tier", got, ok)
	}
	for _, tier := range []keymap.Tier{keymap.TierEarly, keymap.TierLate} {
		if got, ok := km.MatchTier(tier, "alt+f"); ok {
			t.Errorf("alt+f is bound to %q; it must reach the shell", got)
		}
	}
}

// TestCtxMenuAndPalette_YaziRowsGatedOnItsOwnBinary: both rows exist, greyed
// rather than hidden when yazi is missing, and live when it is installed even
// with the git tools absent.
func TestCtxMenuAndPalette_YaziRowsGatedOnItsOwnBinary(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.pluginRegistry = registryWithOverlayTools(t)
	pane := m.curTabs()[0].Root.Left.Pane

	rowEnabled := func() (menu, palette bool) {
		for _, it := range m.buildCtxMenuItems(pane) {
			if it.id == ctxActYazi {
				menu = it.enabled
			}
		}
		palette, found := paletteRowEnabled(m.buildPaletteCommands(), palActYazi)
		if !found {
			t.Fatal("no palette row for yazi")
		}
		return menu, palette
	}

	m.pluginRegistry.Get(overlayPluginYazi).Available = false
	if menu, palette := rowEnabled(); menu || palette {
		t.Errorf("yazi missing: menu=%v palette=%v, want both disabled", menu, palette)
	}

	m.pluginRegistry.Get(overlayPluginYazi).Available = true
	m.pluginRegistry.Get(overlayPluginLazygit).Available = false
	m.pluginRegistry.Get(overlayPluginHunk).Available = false
	if menu, palette := rowEnabled(); !menu || !palette {
		t.Errorf("yazi installed: menu=%v palette=%v, want both enabled", menu, palette)
	}
}

// TestReconcileOverlay_ForeignOverlayForgetsTheAskedDirectory: the remembered
// directory belongs to the overlay THIS client created. An overlay arriving
// that it did not ask for must match on the daemon's CWD alone.
func TestReconcileOverlay_ForeignOverlayForgetsTheAskedDirectory(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "/x")
	tab.overlayAskedCWD = "/x"
	paneMap := map[string]*PaneInfo{
		"pane-new": {ID: "pane-new", Type: overlayPluginYazi, CWD: "/other", Overlay: true},
	}
	m.reconcileOverlayPane(tab, TabInfo{ID: tab.ID, Panes: []string{"pane-new"}}, paneMap, map[string]*PaneModel{}, nil)

	if tab.overlayPane == nil || tab.overlayPane.ID != "pane-new" {
		t.Fatal("setup: the overlay was not adopted")
	}
	if tab.overlayAskedCWD != "" {
		t.Errorf("overlayAskedCWD = %q after adopting a foreign overlay, want empty", tab.overlayAskedCWD)
	}
	if tab.overlayOpenedFor("/x") {
		t.Error("the foreign overlay matched the old asked directory")
	}
	t.Cleanup(tab.overlayPane.Dispose)
}
