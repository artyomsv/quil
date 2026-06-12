package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// overlayTestModel builds a minimal Model with one tab containing one normal
// pane. The fakeSender records every IPC send so tests can assert on it.
func overlayTestModel(t *testing.T, paneCWD string) (*Model, *fakeSender, *TabModel) {
	t.Helper()
	pane := NewPaneModel("pane-n", 1024)
	pane.CWD = paneCWD
	tab := NewTabModel("tab-1", "t")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = pane.ID

	fake := &fakeSender{}
	m := &Model{
		cfg:            config.Default(),
		tabs:           []*TabModel{tab},
		activeTab:      0,
		client:         fake,
		pluginRegistry: registryWithLazygit(t),
	}
	return m, fake, tab
}

// gitRepoDir creates a temp directory with a .git sub-directory, making it
// look like a git repository root. Symlinks are resolved so EvalSymlinks in
// production code doesn't produce a different canonical path.
func gitRepoDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// runCmd executes a tea.Cmd tree, unwrapping BatchMsg so fakeSender records
// all nested sends. Matches the actual tea.BatchMsg type used by Bubble Tea v2.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(c)
		}
	}
}

// decodeSentType returns the msg.Type of the nth IPC message sent.
func decodeSentType(t *testing.T, fake *fakeSender, n int) string {
	t.Helper()
	if n >= len(fake.sent) {
		t.Fatalf("expected at least %d sent messages, got %d", n+1, len(fake.sent))
	}
	return fake.sent[n].Type
}

// decodeSentPayload unmarshals the payload of sent message n into dest.
func decodeSentPayload(t *testing.T, fake *fakeSender, n int, dest any) {
	t.Helper()
	if n >= len(fake.sent) {
		t.Fatalf("expected at least %d sent messages, got %d", n+1, len(fake.sent))
	}
	if err := json.Unmarshal(fake.sent[n].Payload, dest); err != nil {
		t.Fatalf("unmarshal sent[%d].Payload: %v", n, err)
	}
}

// sentTypes returns only the unique message types in sent order (helper for
// assertions that don't care about resize noise).
func sentTypesFiltered(fake *fakeSender, include ...string) []string {
	want := make(map[string]bool, len(include))
	for _, t := range include {
		want[t] = true
	}
	var out []string
	for _, m := range fake.sent {
		if want[m.Type] {
			out = append(out, m.Type)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// handleToggleLazygit tests
// ---------------------------------------------------------------------------

// TestHandleToggleLazygit_VisibleOverlay_Hides: when the overlay is already
// visible, Alt+G hides it without sending any IPC.
func TestHandleToggleLazygit_VisibleOverlay_Hides(t *testing.T) {
	m, fake, tab := overlayTestModel(t, "")
	// Pre-condition: overlay exists and is visible.
	overlay := NewPaneModel("pane-o", 1024)
	tab.overlayPane = overlay
	tab.overlayVisible = true

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	if tab.overlayVisible {
		t.Error("overlayVisible must be false after hiding")
	}
	if len(fake.sent) != 0 {
		t.Errorf("expected no IPC sends on hide, got %d", len(fake.sent))
	}
}

// TestHandleToggleLazygit_NoRepo_NoOverlay_Flashes: when the pane CWD has no
// git repo and no overlay exists, a flash message is set.
func TestHandleToggleLazygit_NoRepo_NoOverlay_Flashes(t *testing.T) {
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, fake, _ := overlayTestModel(t, plain)

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	if m.flashText == "" {
		t.Error("expected a flash message when no git repo and no overlay")
	}
	if len(fake.sent) != 0 {
		t.Errorf("expected no IPC sends, got %d", len(fake.sent))
	}
}

// TestHandleToggleLazygit_NoRepo_ExistingOverlay_Shows: when there is no
// candidate repo but an overlay pane already exists, show it.
func TestHandleToggleLazygit_NoRepo_ExistingOverlay_Shows(t *testing.T) {
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, _, tab := overlayTestModel(t, plain)
	overlay := NewPaneModel("pane-o", 1024)
	tab.overlayPane = overlay
	tab.overlayVisible = false

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	if !tab.overlayVisible {
		t.Error("overlay should become visible when overlay exists but no candidates")
	}
}

// TestHandleToggleLazygit_MatchingRepo_ShowsNoCreate: when the active pane's
// CWD is inside a git repo and an overlay for that repo already exists, show
// it without sending MsgCreatePane or MsgDestroyPane.
func TestHandleToggleLazygit_MatchingRepo_ShowsNoCreate(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)

	overlay := NewPaneModel("pane-o", 1024)
	overlay.CWD = repo
	tab.overlayPane = overlay
	tab.overlayVisible = false

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	if !tab.overlayVisible {
		t.Error("overlay must become visible")
	}
	// Only resize sends are allowed; create/destroy must not fire.
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgCreatePane || msg.Type == ipc.MsgDestroyPane {
			t.Errorf("unexpected IPC %s sent on show-existing overlay", msg.Type)
		}
	}
}

// TestHandleToggleLazygit_SingleRepo_NoOverlay_Creates: when a single repo
// is found and no overlay exists, MsgCreatePane is sent with Overlay:true.
func TestHandleToggleLazygit_SingleRepo_NoOverlay_Creates(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	// pendingOverlayShow must be set for the tab.
	if !m.pendingOverlayShow[tab.ID] {
		t.Error("pendingOverlayShow[tab-1] must be true after creating overlay")
	}
	// MsgCreatePane must be among the sent messages.
	creates := sentTypesFiltered(fake, ipc.MsgCreatePane)
	if len(creates) != 1 {
		t.Fatalf("want 1 MsgCreatePane, got %v (all sent: %v)", len(creates), debugSentTypes(fake))
	}
	var p ipc.CreatePanePayload
	for i, msg := range fake.sent {
		if msg.Type == ipc.MsgCreatePane {
			decodeSentPayload(t, fake, i, &p)
			break
		}
	}
	if p.TabID != "tab-1" {
		t.Errorf("TabID = %q, want tab-1", p.TabID)
	}
	if p.CWD != repo {
		t.Errorf("CWD = %q, want %q", p.CWD, repo)
	}
	if p.Type != "lazygit" {
		t.Errorf("Type = %q, want lazygit", p.Type)
	}
	if !p.Overlay {
		t.Error("Overlay must be true")
	}
	// InstanceArgs must contain ["--path", repo].
	found := false
	for i := 0; i+1 < len(p.InstanceArgs); i++ {
		if p.InstanceArgs[i] == "--path" && p.InstanceArgs[i+1] == repo {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("InstanceArgs %v must contain [--path, %s]", p.InstanceArgs, repo)
	}
}

// TestHandleToggleLazygit_DifferentRepo_DestroysAndCreates: when a single
// repo is found that differs from the existing overlay's repo, MsgDestroyPane
// is sent for the old and MsgCreatePane for the new.
func TestHandleToggleLazygit_DifferentRepo_DestroysAndCreates(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, tab := overlayTestModel(t, repo)

	// Pre-existing overlay for a different repo.
	old := NewPaneModel("pane-old", 1024)
	old.CWD = "/some/other/repo"
	tab.overlayPane = old
	tab.overlayVisible = false

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	destroys := sentTypesFiltered(fake, ipc.MsgDestroyPane)
	creates := sentTypesFiltered(fake, ipc.MsgCreatePane)
	if len(destroys) != 1 {
		t.Errorf("want 1 MsgDestroyPane, got %d (all: %v)", len(destroys), debugSentTypes(fake))
	}
	if len(creates) != 1 {
		t.Errorf("want 1 MsgCreatePane, got %d (all: %v)", len(creates), debugSentTypes(fake))
	}
	// Overlay slot must be cleared (nil) — the new one arrives from the daemon.
	if tab.overlayPane != nil {
		t.Error("overlay slot must be cleared after destroy+create")
	}
}

// TestHandleToggleLazygit_LazygitUnavailable_Flashes: when the lazygit
// plugin reports Available=false, flash "lazygit not installed", no IPC.
func TestHandleToggleLazygit_LazygitUnavailable_Flashes(t *testing.T) {
	repo := gitRepoDir(t)
	m, fake, _ := overlayTestModel(t, repo)
	// Force the plugin unavailable.
	m.pluginRegistry.Get("lazygit").Available = false

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	if m.flashText == "" {
		t.Error("expected flash when lazygit unavailable")
	}
	if len(fake.sent) != 0 {
		t.Errorf("expected no IPC sends, got %d", len(fake.sent))
	}
}

// TestHandleToggleLazygit_TwoCandidates_OpensPicker: when two repos are found
// and no overlay exists, the picker dialog opens with both candidates.
func TestHandleToggleLazygit_TwoCandidates_OpensPicker(t *testing.T) {
	// Build a base directory with two sub-repos.
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

	cmd := m.handleToggleLazygit()
	runCmd(cmd)

	if m.dialog != dialogGitRepoPick {
		t.Errorf("dialog = %v, want dialogGitRepoPick", m.dialog)
	}
	if len(m.repoPickCandidates) < 2 {
		t.Errorf("repoPickCandidates = %v, want at least 2", m.repoPickCandidates)
	}
	if m.dialogCursor != 0 {
		t.Errorf("dialogCursor = %d, want 0", m.dialogCursor)
	}
	if len(fake.sent) != 0 {
		t.Errorf("expected no IPC sends before user picks, got %d", len(fake.sent))
	}
}

// ---------------------------------------------------------------------------
// handleOverlayKey tests
// ---------------------------------------------------------------------------

// TestHandleOverlayKey_ToggleKey_Hides: the toggle key while overlay is
// visible hides it.
func TestHandleOverlayKey_ToggleKey_Hides(t *testing.T) {
	m, _, tab := overlayTestModel(t, "")
	overlay := NewPaneModel("pane-o", 1024)
	tab.overlayPane = overlay
	tab.overlayVisible = true

	// Default binding is "alt+g". Text must be empty so String() →
	// "alt+g" via Keystroke() (real terminals send no Text for alt+rune).
	key := tea.KeyPressMsg{Mod: tea.ModAlt, Code: 'g'}
	cmd := m.handleOverlayKey(key, tab)
	runCmd(cmd)

	if tab.overlayVisible {
		t.Error("toggle key routed through handleOverlayKey must hide the overlay")
	}
}

// TestHandleOverlayKey_AltNum_SwitchesTab: alt+3 in overlay mode returns a
// switchTab-style cmd (non-nil) and changes activeTab.
func TestHandleOverlayKey_AltNum_SwitchesTab(t *testing.T) {
	m, fake, tab := overlayTestModel(t, "")
	// Add a second and third tab so alt+2 and alt+3 are valid.
	m.tabs = append(m.tabs, NewTabModel("tab-2", "b"), NewTabModel("tab-3", "c"))
	overlay := NewPaneModel("pane-o", 1024)
	tab.overlayPane = overlay
	tab.overlayVisible = true

	key := tea.KeyPressMsg{Mod: tea.ModAlt, Code: '3'} // Text must be empty so String() → "alt+3" via Keystroke()
	cmd := m.handleOverlayKey(key, tab)
	runCmd(cmd)

	// switchTab sends a MsgSwitchTab IPC message.
	found := false
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgSwitchTab {
			found = true
			break
		}
	}
	if !found {
		t.Error("alt+3 in overlay mode must send MsgSwitchTab")
	}
}

// TestHandleOverlayKey_PlainRune_ForwardsToOverlay: a plain printable key
// while the overlay is visible must be forwarded to the overlay pane as
// MsgPaneInput.
func TestHandleOverlayKey_PlainRune_ForwardsToOverlay(t *testing.T) {
	m, fake, tab := overlayTestModel(t, "")
	overlay := NewPaneModel("pane-o", 1024)
	overlay.ID = "pane-overlay"
	tab.overlayPane = overlay
	tab.overlayVisible = true

	key := tea.KeyPressMsg{Text: "j"} // plain rune
	cmd := m.handleOverlayKey(key, tab)
	runCmd(cmd)

	// Should have sent a MsgPaneInput to the overlay pane.
	found := false
	for i, msg := range fake.sent {
		if msg.Type == ipc.MsgPaneInput {
			var p ipc.PaneInputPayload
			decodeSentPayload(t, fake, i, &p)
			if p.PaneID == "pane-overlay" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("plain rune in overlay mode must forward MsgPaneInput to overlay pane (sent: %v)", debugSentTypes(fake))
	}
}

// debugSentTypes returns a slice of sent message types for test failure output.
func debugSentTypes(fake *fakeSender) []string {
	var out []string
	for _, m := range fake.sent {
		out = append(out, m.Type)
	}
	return out
}
