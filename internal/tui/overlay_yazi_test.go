package tui

import (
	"encoding/json"
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

// lastDirCheck returns the last dirs_exist_req sent and the folders it asked
// about, failing when there is none.
func lastDirCheck(t *testing.T, fake *fakeSender) (*ipc.Message, []string) {
	t.Helper()
	var req *ipc.Message
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgDirsExistReq {
			req = msg
		}
	}
	if req == nil {
		t.Fatalf("no dirs_exist_req sent: %v", debugSentTypes(fake))
	}
	var p ipc.DirsExistReqPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		t.Fatalf("decode dirs_exist_req: %v", err)
	}
	return req, p.Paths
}

// answerDirCheck plays the daemon's answer to the pending folder check:
// exists=true confirms every folder asked about, false says none exist.
func answerDirCheck(t *testing.T, m *Model, fake *fakeSender, exists bool) {
	t.Helper()
	req, paths := lastDirCheck(t, fake)
	if !m.isOverlayDirAnswer(req.ID) {
		t.Fatal("the answer would not be routed to the overlay (gen or purpose mismatch)")
	}
	var resp ipc.DirsExistRespPayload
	if exists {
		resp.Paths = paths
	}
	runCmd(m.applyOverlayDir(resp))
}

// TestToggleYazi_ChecksTheFolderThenCreatesWithoutGitDiscovery: yazi opens on
// the active pane's own directory, so the key asks the daemon only whether
// that folder exists — never for git repositories, which would answer "no git
// repo here" for most folders — and creates nothing until the answer lands.
// The directory reaches yazi as the spawn CWD alone, so InstanceArgs stay
// empty.
func TestToggleYazi_ChecksTheFolderThenCreatesWithoutGitDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // deliberately NOT a git repository
	m, fake, _ := overlayTestModel(t, dir)

	runCmd(m.handleToggleYazi())

	if got := sentTypesFiltered(fake, ipc.MsgGitReposReq); len(got) != 0 {
		t.Errorf("yazi asked for git repositories: %v", debugSentTypes(fake))
	}
	if _, paths := lastDirCheck(t, fake); len(paths) != 1 || paths[0] != dir {
		t.Errorf("checked %v, want [%s]", paths, dir)
	}
	if got := sentTypesFiltered(fake, ipc.MsgCreatePane); len(got) != 0 {
		t.Fatalf("created before the folder check answered: %v", debugSentTypes(fake))
	}

	answerDirCheck(t, m, fake, true)

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

// TestToggleYazi_MissingFolder_KeepsTheOldOverlay: the daemon's create path
// falls back to another directory for a CWD it cannot use, so a folder that is
// gone must be refused BEFORE the slot is touched — no destroy, no create, the
// old overlay still there, and a message that says why.
func TestToggleYazi_MissingFolder_KeepsTheOldOverlay(t *testing.T) {
	t.Parallel()
	m, fake, tab := overlayTestModel(t, "/deleted/folder")
	old := yaziOverlay(tab, "/somewhere/else", false)

	runCmd(m.handleToggleYazi())
	answerDirCheck(t, m, fake, false)

	if got := sentTypesFiltered(fake, ipc.MsgCreatePane, ipc.MsgDestroyPane); len(got) != 0 {
		t.Errorf("a missing folder touched the slot: %v", debugSentTypes(fake))
	}
	if tab.overlayPane != old {
		t.Error("the old overlay was dropped")
	}
	if m.flashText != "yazi not opened: folder not found" {
		t.Errorf("flash = %q", m.flashText)
	}
	if _, pending := m.pendingOverlayAsk[tab.ID]; pending {
		t.Error("a refused folder was remembered as asked")
	}
}

// TestToggleYazi_CheckFailed_ReportsIt: a failed check is not "the folder is
// gone" — the message says the check failed, and nothing is created.
func TestToggleYazi_CheckFailed_ReportsIt(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, t.TempDir())

	runCmd(m.handleToggleYazi())
	if !m.isOverlayDirAnswer(m.repoScan.gen) {
		t.Fatal("setup: no folder check pending")
	}
	runCmd(m.applyOverlayDir(ipc.DirsExistRespPayload{Error: "busy"}))

	if got := sentTypesFiltered(fake, ipc.MsgCreatePane); len(got) != 0 {
		t.Errorf("created after a failed check: %v", debugSentTypes(fake))
	}
	if m.flashText != "yazi not opened: could not check the folder" {
		t.Errorf("flash = %q", m.flashText)
	}
}

// TestToggleYazi_CheckTimedOut_ReportsIt: the timeout names yazi rather than
// "repo scan timed out", which would describe a request that was never made.
func TestToggleYazi_CheckTimedOut_ReportsIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _ := overlayTestModel(t, dir)

	runCmd(m.handleToggleYazi())
	runCmd(m.applyGitScanTimeout(dir, m.repoScan.gen))

	if m.flashText != "yazi not opened: folder check timed out" {
		t.Errorf("flash = %q", m.flashText)
	}
}

// TestToggleYazi_SupersedesAPendingGitScan: Alt+G on a slow repository, then
// Alt+Shift+F before the git answer lands. The later key wins: the old answer
// must not replace the yazi the user asked for with lazygit.
func TestToggleYazi_SupersedesAPendingGitScan(t *testing.T) {
	t.Parallel()
	repo := gitRepoDir(t)
	m, fake, _ := overlayTestModel(t, repo)

	runCmd(m.handleToggleLazygit())
	oldGen := m.repoScan.gen
	runCmd(m.handleToggleYazi())
	answerDirCheck(t, m, fake, true)
	if p := createdPane(t, fake); p.Type != overlayPluginYazi {
		t.Fatalf("created %q, want yazi", p.Type)
	}

	// The git answer arrives late, for the request yazi superseded.
	fake.sent = nil
	runCmd(m.applyGitRepos(ipc.GitReposRespPayload{CWD: repo, Repos: []string{repo}}, oldGen))
	runCmd(m.applyGitScanTimeout(repo, oldGen))

	if got := sentTypesFiltered(fake, ipc.MsgCreatePane, ipc.MsgDestroyPane); len(got) != 0 {
		t.Errorf("the superseded git scan replaced yazi: %v", debugSentTypes(fake))
	}
	if m.dialog == dialogGitRepoPick {
		t.Error("the superseded git scan opened the repository picker")
	}
	if m.flashText == "repo scan timed out" {
		t.Error("the superseded scan's timeout still reported")
	}
}

// TestDirsExistAnswer_RecentListStillGetsItsOwn: the overlay check and the
// setup dialog's recent list share the response type. A recent-list answer
// must not be taken for the overlay's.
func TestDirsExistAnswer_RecentListStillGetsItsOwn(t *testing.T) {
	t.Parallel()
	m, _, _ := overlayTestModel(t, t.TempDir())
	m.recentScan = recentScanState{gen: "recent-1"}
	runCmd(m.handleToggleYazi())

	if m.isOverlayDirAnswer("recent-1") {
		t.Error("a recent-list answer was routed to the overlay")
	}
	if !m.isOverlayDirAnswer(m.repoScan.gen) {
		t.Error("the overlay's own answer is not routed to it")
	}
}

// TestToggleLazygit_StillAsksTheDaemonForRepos: the folder shortcut is
// yazi's alone. The git tools keep resolving repositories on the daemon.
func TestToggleLazygit_StillAsksTheDaemonForRepos(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, t.TempDir())

	runCmd(m.handleToggleLazygit())

	if got := sentTypesFiltered(fake, ipc.MsgGitReposReq); len(got) != 1 {
		t.Errorf("want one git_repos_req, got %v", debugSentTypes(fake))
	}
	if got := sentTypesFiltered(fake, ipc.MsgDirsExistReq, ipc.MsgCreatePane); len(got) != 0 {
		t.Errorf("lazygit took yazi's path: %v", debugSentTypes(fake))
	}
}

// TestToggleYazi_NoDirectory_FlashesWithoutCreating: a pane that has not
// reported a directory gives yazi nothing to open. The message names yazi and
// the reason, not "no git repo".
func TestToggleYazi_NoDirectory_FlashesWithoutCreating(t *testing.T) {
	t.Parallel()
	m, fake, _ := overlayTestModel(t, "")

	runCmd(m.handleToggleYazi())

	if got := sentTypesFiltered(fake, ipc.MsgCreatePane, ipc.MsgDirsExistReq); len(got) != 0 {
		t.Errorf("sent with no directory: %v", debugSentTypes(fake))
	}
	if !strings.HasPrefix(m.flashText, "yazi not opened") {
		t.Errorf("flash = %q, want a yazi-specific reason", m.flashText)
	}
}

// TestToggleYazi_SameDirectory_ShowsTheRunningOverlay is the retention rule:
// pressing the key again in the same folder shows the yazi already running,
// with its state, at once — no round trip, no new process.
func TestToggleYazi_SameDirectory_ShowsTheRunningOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fake, tab := overlayTestModel(t, dir)
	yaziOverlay(tab, dir, false)

	runCmd(m.handleToggleYazi())

	if !tab.overlayVisible {
		t.Error("the running yazi overlay must be shown")
	}
	if got := sentTypesFiltered(fake, ipc.MsgCreatePane, ipc.MsgDestroyPane, ipc.MsgDirsExistReq); len(got) != 0 {
		t.Errorf("same folder must show the overlay as it is: %v", debugSentTypes(fake))
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
	answerDirCheck(t, m, fake, true)

	got := sentTypesFiltered(fake, ipc.MsgDestroyPane, ipc.MsgCreatePane)
	if len(got) != 2 || got[0] != ipc.MsgDestroyPane || got[1] != ipc.MsgCreatePane {
		t.Fatalf("want destroy then create, got %v", debugSentTypes(fake))
	}
	if p := createdPane(t, fake); p.CWD != dir {
		t.Errorf("new overlay CWD = %q, want %q", p.CWD, dir)
	}
	if ask := m.pendingOverlayAsk[tab.ID]; ask.plugin != overlayPluginYazi || ask.cwd != dir {
		t.Errorf("pendingOverlayAsk = %+v, want yazi in %q", ask, dir)
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
	answerDirCheck(t, m, fake, true)

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
	answerDirCheck(t, m, fake, true)

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
	answerDirCheck(t, m, fake, true)
	if p := createdPane(t, fake); p.Type != overlayPluginYazi {
		t.Errorf("created %q, want yazi", p.Type)
	}
}

// TestHandleKey_YaziKey_DispatchesThroughUpdate drives the shipped default
// through Update, so the early-tier case arm is what is under test, not only
// the handler it calls.
func TestHandleKey_YaziKey_DispatchesThroughUpdate(t *testing.T) {
	t.Parallel()
	m, tab := hunkOverlayModel(t)
	yaziOverlay(tab, "", false)
	m.pluginRegistry = registryWithOverlayTools(t)

	m.Update(tea.KeyPressMsg{Mod: tea.ModAlt | tea.ModShift, Code: 'f'})

	// No pane has a CWD in this fixture, so the key shows the hidden yazi
	// overlay (step 3) — which only happens if the key reached the handler.
	if !tab.overlayVisible {
		t.Error("alt+shift+f did not reach the yazi overlay handler")
	}
}

// TestYaziDefault_IsAltShiftF_NotAltF pins the default chord. Plain Alt+F is
// readline forward-word (keyToBytes sends ESC-f for Option-as-Meta users, and
// Terminal.app sends it for Option+Right), so a default on it would take word
// navigation from every shell pane.
//
// alt+F is bound too: macOS Terminal.app with Option as Meta reports
// Option+Shift+F as {Code:'F', ModAlt} with no ModShift, so without it the
// advertised key would be typed into the pane there.
func TestYaziDefault_IsAltShiftF_NotAltF(t *testing.T) {
	t.Parallel()
	km, _ := buildKeymap(config.Default().Keybindings)
	for _, press := range []tea.KeyPressMsg{
		{Mod: tea.ModAlt | tea.ModShift, Code: 'f'}, // kitty protocol, iTerm2, Windows
		{Mod: tea.ModAlt, Code: 'F'},                // Terminal.app ESC F
	} {
		key := press.String()
		if got, ok := km.MatchTier(keymap.TierEarly, key); !ok || got != "pane.toggle_yazi" {
			t.Errorf("%q = (%q, %v), want pane.toggle_yazi in the early tier", key, got, ok)
		}
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

// TestReconcileOverlay_AskedDirectoryFollowsOnlyItsOwnOverlay: the folder
// createOverlay asked for is attached to the overlay that create produced —
// this client's own, of the same tool — and to nothing else. A foreign
// overlay, or one of another tool, matches on the daemon's CWD alone.
func TestReconcileOverlay_AskedDirectoryFollowsOnlyItsOwnOverlay(t *testing.T) {
	t.Parallel()
	arrive := func(t *testing.T, m *Model, tab *TabModel, id, typ string) {
		t.Helper()
		paneMap := map[string]*PaneInfo{id: {ID: id, Type: typ, CWD: "/private/x", Overlay: true}}
		m.reconcileOverlayPane(tab, TabInfo{ID: tab.ID, Panes: []string{id}}, paneMap, map[string]*PaneModel{}, nil)
		if tab.overlayPane == nil || tab.overlayPane.ID != id {
			t.Fatal("setup: the overlay was not adopted")
		}
		t.Cleanup(tab.overlayPane.Dispose)
	}

	t.Run("own create of the same tool", func(t *testing.T) {
		m, _, tab := overlayTestModel(t, "/x")
		m.pendingOverlayShow = map[string]bool{tab.ID: true}
		m.pendingOverlayAsk = map[string]overlayAsk{tab.ID: {plugin: overlayPluginYazi, cwd: "/x"}}
		arrive(t, m, tab, "pane-own", overlayPluginYazi)
		if !tab.overlayOpenedFor("/x") {
			t.Error("this client's own yazi does not match the folder it asked for")
		}
		if _, left := m.pendingOverlayAsk[tab.ID]; left {
			t.Error("the ask outlived the arrival it was for")
		}
	})
	t.Run("foreign overlay", func(t *testing.T) {
		m, _, tab := overlayTestModel(t, "/x")
		tab.overlayAskedCWD = "/x"
		arrive(t, m, tab, "pane-foreign", overlayPluginYazi)
		if tab.overlayOpenedFor("/x") {
			t.Error("a foreign overlay matched the old asked directory")
		}
	})
	t.Run("own create, other tool arrived", func(t *testing.T) {
		m, _, tab := overlayTestModel(t, "/x")
		m.pendingOverlayShow = map[string]bool{tab.ID: true}
		m.pendingOverlayAsk = map[string]overlayAsk{tab.ID: {plugin: overlayPluginYazi, cwd: "/x"}}
		arrive(t, m, tab, "pane-lg", overlayPluginLazygit)
		if tab.overlayAskedCWD != "" {
			t.Errorf("overlayAskedCWD = %q on a lazygit that yazi's ask did not produce", tab.overlayAskedCWD)
		}
	})
}
