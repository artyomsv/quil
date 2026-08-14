package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitdiscover"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// A tab has ONE overlay slot, shared by every overlay tool. These tests cover
// what that sharing costs: each decision in the state machine has to key on
// WHICH tool was asked for, not merely on whether an overlay exists. Helpers
// (overlayTestModel, runCmd, gitRepoDir, decodeSentPayload, …) live in
// overlay_test.go.

// registryWithOverlayTools returns a registry holding every plugin that can
// own a tab's overlay slot, all available. Registry has no Register method —
// the established pattern is temp TOML + LoadFromDir, then flip the exported
// Available field via Get (see registryWithLazygit).
func registryWithOverlayTools(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"lazygit.toml": "[plugin]\nname = \"lazygit\"\n\n[command]\ncmd = \"lazygit\"\nprompts_cwd = true\ndiscover = \"git\"\n",
		"hunk.toml":    "[plugin]\nname = \"hunk\"\n\n[command]\ncmd = \"hunk\"\nargs = [\"diff\"]\nprompts_cwd = true\ndiscover = \"git\"\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	for name := range files {
		r.Get(strings.TrimSuffix(name, ".toml")).Available = true
	}
	return r
}

// toggleOverlayWithDiscovery is toggleWithDiscovery for an arbitrary overlay
// plugin — same reasoning, see that helper.
func toggleOverlayWithDiscovery(m *Model, tab *TabModel, cwd, plugin string) tea.Cmd {
	return m.resolveOverlay(tab, gitdiscover.Candidates(context.Background(), cwd), plugin)
}

// createdPane returns the payload of the single MsgCreatePane sent, failing if
// there is not exactly one.
func createdPane(t *testing.T, fake *fakeSender) ipc.CreatePanePayload {
	t.Helper()
	var out ipc.CreatePanePayload
	found := 0
	for i, msg := range fake.sent {
		if msg.Type != ipc.MsgCreatePane {
			continue
		}
		found++
		decodeSentPayload(t, fake, i, &out)
	}
	if found != 1 {
		t.Fatalf("want exactly 1 MsgCreatePane, got %d (all: %v)", found, debugSentTypes(fake))
	}
	return out
}

// TestResolveOverlay_Hunk_CreatesWithNoInstanceArgs: hunk has no --path flag,
// so the repo is carried by CWD alone — and InstanceArgs must stay EMPTY,
// because resolveSpawnArgs treats any InstanceArgs as a replacement for the
// plugin's base args, which is where hunk's `diff` subcommand lives. Copying
// lazygit's ["--path", repo] shape here would spawn `hunk --path <repo>`.
func TestResolveOverlay_Hunk_CreatesWithNoInstanceArgs(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)

	runCmd(toggleOverlayWithDiscovery(m, tab, repo, "hunk"))

	p := createdPane(t, fake)
	if p.Type != "hunk" {
		t.Errorf("Type = %q, want hunk", p.Type)
	}
	if p.CWD != repo {
		t.Errorf("CWD = %q, want %q", p.CWD, repo)
	}
	if !p.Overlay {
		t.Error("Overlay must be true")
	}
	if len(p.InstanceArgs) != 0 {
		t.Errorf("InstanceArgs = %v, want empty (they would replace the `diff` base arg)", p.InstanceArgs)
	}
}

// TestToggleHunk_VisibleHunkOverlay_Hides: the same tool's key hides its own
// overlay, exactly as Alt+G does for lazygit.
func TestToggleHunk_VisibleHunkOverlay_Hides(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "")
	overlay := NewPaneModel("pane-o", 1024)
	overlay.Type = "hunk"
	tab.overlayPane = overlay
	tab.overlayVisible = true

	runCmd(m.handleToggleHunk())

	if tab.overlayVisible {
		t.Error("Alt+H over a visible hunk overlay must hide it")
	}
}

// TestToggleHunk_VisibleLazygitOverlay_DoesNotHide: with one slot per tab,
// Alt+H while lazygit is on screen must REPLACE it, not hide it. The hide
// branch keys on the overlay's own plugin type; without that check the other
// tool's key reads as "toggle off" and the user has to press it twice to get
// anywhere.
func TestToggleHunk_VisibleLazygitOverlay_DoesNotHide(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	overlay := NewPaneModel("pane-o", 1024)
	overlay.Type = "lazygit"
	overlay.CWD = repo
	tab.overlayPane = overlay
	tab.overlayVisible = true

	runCmd(m.handleToggleHunk())

	if !tab.overlayVisible {
		t.Error("Alt+H must not hide another tool's overlay")
	}
	if got := sentTypesFiltered(fake, ipc.MsgGitReposReq); len(got) != 1 {
		t.Errorf("want 1 MsgGitReposReq (the replace path resolving candidates), got %v", debugSentTypes(fake))
	}
}

// TestToggleHunk_RoundTrip_CreatesHunkNotLazygit drives the WHOLE async path:
// key press → daemon request → daemon answer → create. Discovery runs on the
// daemon, so the requesting plugin has to survive the round trip in
// repoScanState; a version that recovered the plugin from anything else would
// still pass every synchronous test here and open lazygit when the user
// pressed Alt+H.
func TestToggleHunk_RoundTrip_CreatesHunkNotLazygit(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, fake, _ := overlayTestModel(t, repo)

	runCmd(m.handleToggleHunk())
	if m.repoScan.gen == "" {
		t.Fatal("expected an in-flight repo scan after Alt+H")
	}
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: repo, Repos: []string{repo}}, m.repoScan.gen))

	if p := createdPane(t, fake); p.Type != "hunk" {
		t.Errorf("Type = %q, want hunk — the requesting plugin was lost across the round trip", p.Type)
	}
}

// TestResolveOverlay_SameRepoOtherTool_Replaces: the "candidate matches the
// existing overlay" fast path must compare the TOOL as well as the repo.
// Matching on the repo alone shows lazygit when the user asked for hunk.
func TestResolveOverlay_SameRepoOtherTool_Replaces(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	overlay := NewPaneModel("pane-lg", 1024)
	overlay.Type = "lazygit"
	overlay.CWD = repo
	tab.overlayPane = overlay
	tab.overlayVisible = false

	runCmd(toggleOverlayWithDiscovery(m, tab, repo, "hunk"))

	if len(sentTypesFiltered(fake, ipc.MsgDestroyPane)) != 1 {
		t.Errorf("want the lazygit overlay destroyed, got %v", debugSentTypes(fake))
	}
	if p := createdPane(t, fake); p.Type != "hunk" {
		t.Errorf("Type = %q, want hunk", p.Type)
	}
}

// TestResolveOverlay_NoCandidatesOtherTool_Flashes: with no repo found, the
// "show the existing overlay anyway" fallback only applies to the tool that
// was asked for. Showing lazygit in response to Alt+H answers a question
// nobody asked.
func TestResolveOverlay_NoCandidatesOtherTool_Flashes(t *testing.T) {
	t.Parallel()
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, fake, tab := overlayTestModel(t, plain)
	overlay := NewPaneModel("pane-lg", 1024)
	overlay.Type = "lazygit"
	tab.overlayPane = overlay
	tab.overlayVisible = false

	runCmd(toggleOverlayWithDiscovery(m, tab, plain, "hunk"))

	if tab.overlayVisible {
		t.Error("Alt+H with no repo must not show the lazygit overlay")
	}
	if m.flashText == "" {
		t.Error("expected a flash message")
	}
	if len(fake.sent) != 0 {
		t.Errorf("expected no IPC sends, got %v", debugSentTypes(fake))
	}
}

// TestResolveOverlay_HunkUnavailable_NamesHunk: the availability gate must
// report the tool the user actually pressed a key for.
func TestResolveOverlay_HunkUnavailable_NamesHunk(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)
	m.pluginRegistry.Get("hunk").Available = false

	runCmd(toggleOverlayWithDiscovery(m, tab, repo, "hunk"))

	if !strings.Contains(m.flashText, "hunk") {
		t.Errorf("flash = %q, want it to name hunk", m.flashText)
	}
	if len(fake.sent) != 0 {
		t.Errorf("expected no IPC sends, got %v", debugSentTypes(fake))
	}
}

// TestResolveOverlay_MultipleRepos_PickerCreatesTheRequestingPlugin: the
// picker is opened by one tool's key and committed later by Enter, so the
// plugin has to be remembered alongside the candidates.
func TestResolveOverlay_MultipleRepos_PickerCreatesTheRequestingPlugin(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		if err := os.MkdirAll(filepath.Join(base, name, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	m, fake, _ := overlayTestModel(t, base)

	runCmd(toggleOverlayWithDiscovery(m, m.activeTabModel(), base, "hunk"))
	if m.dialog != dialogGitRepoPick {
		t.Fatalf("dialog = %v, want dialogGitRepoPick", m.dialog)
	}

	out, cmd := m.handleGitRepoPickKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	*m = out.(Model)
	runCmd(cmd)

	if p := createdPane(t, fake); p.Type != "hunk" {
		t.Errorf("Type = %q, want hunk — the picker forgot which key opened it", p.Type)
	}
}

// TestHandleOverlayKey_HunkKey_HidesHunkOverlay: the hunk toggle has to be in
// the while-visible allow-list, or it reaches the overlay's PTY as input.
func TestHandleOverlayKey_HunkKey_HidesHunkOverlay(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "")
	overlay := NewPaneModel("pane-o", 1024)
	overlay.Type = "hunk"
	tab.overlayPane = overlay
	tab.overlayVisible = true

	// Text must be empty so String() → "alt+d" via Keystroke().
	runCmd(m.handleOverlayKey(tea.KeyPressMsg{Mod: tea.ModAlt, Code: 'd'}, tab))

	if tab.overlayVisible {
		t.Error("the hunk toggle routed through handleOverlayKey must hide the hunk overlay")
	}
}

// TestHandleOverlayKey_AltH_ReachesThePTY guards the invariant the default
// binding was chosen around: Alt+H is deliberately unbound at the global level
// so it reaches the child process (see .claude/rules/tui-rendering.md and the
// PaneLeft comment in config.go). If a future default moves the hunk toggle
// onto alt+h, this fails instead of silently swallowing the key in every pane.
func TestHandleOverlayKey_AltH_ReachesThePTY(t *testing.T) {
	t.Parallel()
	m, _, tab := overlayTestModel(t, "")
	overlay := NewPaneModel("pane-o", 1024)
	overlay.Type = "hunk"
	tab.overlayPane = overlay
	tab.overlayVisible = true

	runCmd(m.handleOverlayKey(tea.KeyPressMsg{Mod: tea.ModAlt, Code: 'h'}, tab))

	if !tab.overlayVisible {
		t.Error("alt+h must fall through to the PTY, not toggle the overlay")
	}
}

// ---------------------------------------------------------------------------
// Entry points: command palette, context menu, shortcuts help
// ---------------------------------------------------------------------------

// hunkOverlayModel returns a two-pane model whose active tab holds a HIDDEN
// hunk overlay and whose panes have no CWD.
//
// That state is what makes the two dispatches distinguishable without a live
// daemon: with no repo to discover, Alt+H shows the hidden hunk overlay (step
// 3's same-tool fallback) while Alt+G only flashes. Hidden rather than visible
// because an overlay on screen swallows the mouse, and the context-menu test
// has to right-click to open its menu. Neither path consults the plugin
// registry, so the fixture's nil one is fine — these tests are about DISPATCH,
// not discovery.
func hunkOverlayModel(t *testing.T) (*Model, *TabModel) {
	t.Helper()
	m := newSplitDragTestModel(t)
	tab := m.curTabs()[0]
	overlay := NewPaneModel("pane-hunk", 1024)
	overlay.Type = overlayPluginHunk
	tab.overlayPane = overlay
	tab.overlayVisible = false
	return m, tab
}

// TestBuildPaletteCommands_HunkGatedOnItsOwnBinary: the palette row must track
// hunk's availability, not lazygit's. One registry, two independently
// installable tools — reading the wrong one offers a row that dead-ends.
func TestBuildPaletteCommands_HunkGatedOnItsOwnBinary(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.pluginRegistry = registryWithOverlayTools(t)
	m.pluginRegistry.Get(overlayPluginHunk).Available = false

	if enabled, found := paletteRowEnabled(m.buildPaletteCommands(), palActHunk); !found {
		t.Fatal("no palette row for hunk")
	} else if enabled {
		t.Error("hunk row must be disabled when the binary is missing")
	}

	m.pluginRegistry.Get(overlayPluginHunk).Available = true
	m.pluginRegistry.Get(overlayPluginLazygit).Available = false
	if enabled, _ := paletteRowEnabled(m.buildPaletteCommands(), palActHunk); !enabled {
		t.Error("hunk row must be enabled when hunk is installed, regardless of lazygit")
	}
}

func paletteRowEnabled(cmds []paletteCommand, action paletteAction) (enabled, found bool) {
	for _, c := range cmds {
		if c.action == action {
			return c.enabled, true
		}
	}
	return false, false
}

// TestPalette_ExecuteHunk_ShowsTheHunkOverlay: the palette row has to reach
// handleToggleHunk. Showing the hidden hunk overlay is the observable that
// separates the two dispatches — palActLazygit against the same state finds no
// lazygit overlay to show and only flashes.
func TestPalette_ExecuteHunk_ShowsTheHunkOverlay(t *testing.T) {
	t.Parallel()
	m, tab := hunkOverlayModel(t)

	// The returned cmd is deliberately not run: showOverlay flips visibility
	// synchronously, and this fixture has no IPC client for the resize and
	// visibility sends that ride along.
	m.executePaletteCommand(paletteCommand{action: palActHunk, label: "Open hunk", enabled: true})

	if !tab.overlayVisible {
		t.Error("the palette's hunk row must route to handleToggleHunk")
	}
}

// TestCtxMenu_HunkRowGatedOnItsOwnBinary mirrors the palette gate: the row is
// offered greyed rather than hidden, exactly like the lazygit row.
func TestCtxMenu_HunkRowGatedOnItsOwnBinary(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.pluginRegistry = registryWithOverlayTools(t)
	m.pluginRegistry.Get(overlayPluginHunk).Available = false
	pane := m.curTabs()[0].Root.Left.Pane

	items := m.buildCtxMenuItems(pane)
	var row *ctxMenuItem
	for i := range items {
		if items[i].id == ctxActHunk {
			row = &items[i]
		}
	}
	if row == nil {
		t.Fatal("no context-menu row for hunk")
	}
	if row.enabled {
		t.Error("hunk row must be disabled when the binary is missing")
	}

	m.pluginRegistry.Get(overlayPluginHunk).Available = true
	for _, it := range m.buildCtxMenuItems(pane) {
		if it.id == ctxActHunk && !it.enabled {
			t.Error("hunk row must be enabled when the binary is present")
		}
	}
}

// TestCtxMenu_ExecuteHunk_ShowsTheHunkOverlay: same dispatch check as the
// palette's, for the menu row. The menu is opened by a real right-click first
// — executeCtxMenuItem refuses when its target is not in the active tab, so a
// row executed against a never-opened menu is inert for reasons that have
// nothing to do with the dispatch under test.
func TestCtxMenu_ExecuteHunk_ShowsTheHunkOverlay(t *testing.T) {
	t.Parallel()
	m, tab := hunkOverlayModel(t)

	updated, _ := m.Update(tea.MouseClickMsg{X: 70, Y: 10, Button: tea.MouseRight})
	got := updated.(Model)
	// The returned cmd is deliberately not run — see the palette test above.
	got.executeCtxMenuItem(ctxMenuItem{id: ctxActHunk, label: "Open hunk", enabled: true})

	if !tab.overlayVisible {
		t.Error("the context menu's hunk row must route to handleToggleHunk")
	}
}

// TestShortcutsDialog_ListsTheHunkBinding: a keybinding nobody can discover is
// a keybinding nobody uses — the help dialog is the only place the default is
// written down inside the app.
func TestShortcutsDialog_ListsTheHunkBinding(t *testing.T) {
	t.Parallel()
	m := newSplitDragTestModel(t)
	m.cfg = config.Default()

	out := stripANSI(m.renderShortcutsDialog())
	if !strings.Contains(out, m.cfg.Keybindings.ToggleHunk) {
		t.Errorf("shortcuts dialog does not mention %q", m.cfg.Keybindings.ToggleHunk)
	}
	if !strings.Contains(strings.ToLower(out), "hunk") {
		t.Error("shortcuts dialog does not name the hunk overlay")
	}
}

// TestRenderGitRepoPick_NamesTheRequestingTool: the picker asks which repo to
// open — it has to say which TOOL it will open there, or Alt+H shows a dialog
// titled "Open lazygit for which repo?" and then spawns hunk.
func TestRenderGitRepoPick_NamesTheRequestingTool(t *testing.T) {
	t.Parallel()
	m := Model{
		dialog:             dialogGitRepoPick,
		repoPickCandidates: []string{"/repo-a", "/repo-b"},
		repoPickPlugin:     overlayPluginHunk,
	}

	out := strings.ToLower(stripANSI(m.renderGitRepoPickDialog()))
	if !strings.Contains(out, overlayPluginHunk) {
		t.Errorf("picker title does not name hunk: %q", out)
	}
	if strings.Contains(out, overlayPluginLazygit) {
		t.Errorf("picker opened by Alt+H must not name lazygit: %q", out)
	}
}

// TestUpdate_HunkKey_ReachesTheHunkToggle drives the key through Model.Update's
// own switch rather than calling the handler.
//
// handleToggleHunk being correct says nothing about it being WIRED: a missing
// case in the main key switch leaves every handler test green while the key
// does nothing at all (or, worse, falls through to the PTY as an escape
// sequence). The overlay-visible path has its own coverage in handleOverlayKey;
// this is the hidden-overlay path, which is the one the main switch owns.
func TestUpdate_HunkKey_ReachesTheHunkToggle(t *testing.T) {
	t.Parallel()
	m, tab := hunkOverlayModel(t)

	// Text must be empty so String() → "alt+d" via Keystroke(), matching what a
	// real terminal sends for an alt+rune chord.
	m.Update(tea.KeyPressMsg{Mod: tea.ModAlt, Code: 'd'})

	if !tab.overlayVisible {
		t.Error("the hunk toggle through Update must reach handleToggleHunk")
	}
}

// hasClearScreen reports whether a cmd tree emits tea.ClearScreen, walking
// nested batches the way runCmd does.
func hasClearScreen(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if msg == tea.ClearScreen() {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if hasClearScreen(c) {
				return true
			}
		}
	}
	return false
}

// TestResolveOverlay_ReplacingAVisibleOverlay_Repaints: swapping tools while
// the outgoing one is ON SCREEN takes the frame from a full-tab overlay back to
// the tab's normal split layout, and the new overlay is a daemon round trip
// away — so that intermediate frame has to be repainted.
//
// Both other visibility transitions in this file already pair with
// ClearScreen (the hide branch and showOverlay). Replacing a VISIBLE overlay
// was unreachable before the slot was shared — step 1 hid it first — so
// createOverlay never needed one, and the asymmetry is exactly what the swap
// path introduced.
func TestResolveOverlay_ReplacingAVisibleOverlay_Repaints(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, _, tab := overlayTestModel(t, repo)
	overlay := NewPaneModel("pane-lg", 1024)
	overlay.Type = overlayPluginLazygit
	overlay.CWD = "/some/other/repo"
	tab.overlayPane = overlay
	tab.overlayVisible = true

	cmd := toggleOverlayWithDiscovery(m, tab, repo, overlayPluginHunk)

	if !hasClearScreen(cmd) {
		t.Error("replacing a visible overlay must repaint — the frame goes from full-tab overlay back to the split layout")
	}
}

// TestResolveOverlay_ReplacingAHiddenOverlay_DoesNotRepaint: the converse. A
// hidden overlay is not on screen, so nothing about the current frame changed
// and a ClearScreen would be a gratuitous full repaint on every Alt+G that
// swaps repos — the common case this path was originally written for.
func TestResolveOverlay_ReplacingAHiddenOverlay_DoesNotRepaint(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, _, tab := overlayTestModel(t, repo)
	overlay := NewPaneModel("pane-lg", 1024)
	overlay.Type = overlayPluginLazygit
	overlay.CWD = "/some/other/repo"
	tab.overlayPane = overlay
	tab.overlayVisible = false

	cmd := toggleOverlayWithDiscovery(m, tab, repo, overlayPluginHunk)

	if hasClearScreen(cmd) {
		t.Error("replacing a hidden overlay changes nothing on screen; no repaint should be emitted")
	}
}
