package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

func TestPaneVTSize(t *testing.T) {
	cases := []struct {
		name                     string
		wide                     bool
		rectW, rectH, canW, canH int
		wantCols, wantRows       int
	}{
		{"normal pane uses rect", false, 60, 20, 200, 50, 58, 18},
		{"canvas pane uses canvas", true, 60, 20, 200, 50, 198, 48},
		{"canvas degenerate clamps", true, 60, 20, 1, 1, 1, 1},
		{"normal degenerate clamps", false, 2, 2, 200, 50, 1, 1},
		{"zero canvas falls back to rect", true, 60, 20, 0, 0, 58, 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := paneVTSize(tc.wide, tc.rectW, tc.rectH, tc.canW, tc.canH)
			if c != tc.wantCols || r != tc.wantRows {
				t.Errorf("got %dx%d, want %dx%d", c, r, tc.wantCols, tc.wantRows)
			}
		})
	}
}

// Zoom must not resize a canvas pane: the grid resize and the focus-mode
// resize must produce the same canvas-derived VT size. This is the core
// invariant of the wide-canvas design — Ctrl+E stops being a PTY resize.
func TestTabResize_CanvasPane_FocusToggleKeepsVTSize(t *testing.T) {
	a := NewPaneModel("a", 4096)
	defer a.Dispose()
	a.WideCanvas = true
	b := NewPaneModel("b", 4096)
	defer b.Dispose()

	tab := NewTabModel("t", "T")
	tab.Root = NewLeaf(a)
	ph := tab.Root.SplitLeaf("a", SplitHorizontal)
	ph.Pane = b
	tab.ActivePane = "a"

	tab.SetCanvas(200, 50)
	tab.Resize(200, 50)
	wantW, wantH := 198, 48
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Fatalf("grid: canvas pane VT %dx%d, want %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}
	if b.vt.Width() >= wantW {
		t.Fatalf("non-canvas pane VT width %d must track its rect, not the canvas", b.vt.Width())
	}

	tab.ToggleFocus()
	tab.Resize(200, 50)
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Errorf("focus: canvas pane VT %dx%d, want unchanged %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}

	tab.ExitFocus()
	tab.Resize(200, 50)
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Errorf("back to grid: canvas pane VT %dx%d, want unchanged %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}
}

// End-to-end regression for the flag pipeline observed broken in dev
// smoke testing (2026-07-05): registry flag → applyWorkspaceState
// reconciliation → resizeTabs → canvas-sized VT.
func TestApplyWorkspaceState_CanvasFlagFlowsToVTSize(t *testing.T) {
	dir := t.TempDir()
	toml := `
[plugin]
name = "claude-code"
schema_version = 7
[command]
cmd = "true"
[display]
wide_canvas = true
`
	if err := os.WriteFile(filepath.Join(dir, "claude-code.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if p := reg.Get("claude-code"); p == nil || !p.Display.WideCanvas {
		t.Fatal("setup: registry must resolve wide_canvas for claude-code")
	}

	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		pluginRegistry: reg,
		mcpHighlights:  make(map[string]bool),
		attached:       true,
		width:          209,
		height:         58,
	}
	state := WorkspaceStateMsg{
		ActiveTab: "t1",
		Tabs: []TabInfo{{
			ID:    "t1",
			Name:  "AI",
			Panes: []string{"pane-c1", "pane-c2"},
		}},
		Panes: []PaneInfo{
			{ID: "pane-c1", TabID: "t1", Type: "claude-code"},
			{ID: "pane-c2", TabID: "t1", Type: "claude-code"},
		},
	}
	m.applyWorkspaceState(state)
	m.resizeTabs()

	tab := m.tabs[0]
	leaves := tab.Leaves()
	if len(leaves) != 2 {
		t.Fatalf("leaves = %d, want 2", len(leaves))
	}
	for _, p := range leaves {
		if !p.WideCanvas {
			t.Errorf("pane %s: WideCanvas=false after reconciliation with flagged registry", p.ID)
		}
		wantW, wantH := 207, 54 // canvas (209, 56) minus border
		if p.vt.Width() != wantW || p.vt.Height() != wantH {
			t.Errorf("pane %s VT %dx%d, want canvas %dx%d (rect was %dx%d)",
				p.ID, p.vt.Width(), p.vt.Height(), wantW, wantH, p.Width, p.Height)
		}
	}
}

// flaggedCanvasRegistry returns a registry whose claude-code plugin has
// wide_canvas = true (mirrors the shipped default) for reconciliation tests.
func flaggedCanvasRegistry(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	toml := "[plugin]\nname = \"claude-code\"\nschema_version = 7\n[command]\ncmd = \"true\"\n[display]\nwide_canvas = true\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	return reg
}

// The cold-restart path (daemon restart with a saved layout tree) goes
// through restoreTabLayout, not the "new pane" branch — this is the exact
// path the 2026-07-05 regression left rect-sized. Assert the flag reaches
// the VT there too.
func TestApplyWorkspaceState_RestorePath_CanvasFlag(t *testing.T) {
	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		pluginRegistry: flaggedCanvasRegistry(t),
		mcpHighlights:  make(map[string]bool),
		attached:       true,
		width:          209,
		height:         58,
	}
	// Build a saved single-pane layout tree so applyWorkspaceState takes the
	// restoreTabLayout branch (tab absent locally + non-empty Layout).
	layout, err := MarshalLayout(NewLeaf(NewPaneModel("pane-r1", 4096)))
	if err != nil {
		t.Fatalf("MarshalLayout: %v", err)
	}
	state := WorkspaceStateMsg{
		ActiveTab: "t1",
		Tabs:      []TabInfo{{ID: "t1", Name: "AI", Panes: []string{"pane-r1"}, Layout: layout}},
		Panes:     []PaneInfo{{ID: "pane-r1", TabID: "t1", Type: "claude-code"}},
	}
	m.applyWorkspaceState(state)
	m.resizeTabs()

	leaves := m.tabs[0].Leaves()
	if len(leaves) != 1 {
		t.Fatalf("leaves = %d, want 1", len(leaves))
	}
	if !leaves[0].WideCanvas {
		t.Error("restore path (restoreTabLayout) left WideCanvas=false — the 2026-07-05 regression")
	}
	if w := leaves[0].vt.Width(); w != 207 {
		t.Errorf("restored canvas pane VT width %d, want 207", w)
	}
}

// Mid-session plugin migration reloads the registry; a subsequent broadcast
// reconciling an ALREADY-present pane (the resync-in-tree branch) must pick
// up the freshly-true flag. Reproduces the regression scenario the
// syncPaneMeta doc comment describes.
func TestApplyWorkspaceState_MidSessionFlip_CanvasFlag(t *testing.T) {
	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		pluginRegistry: plugin.NewRegistry(), // no wide_canvas yet (pre-migration)
		mcpHighlights:  make(map[string]bool),
		attached:       true,
		width:          209,
		height:         58,
	}
	state := WorkspaceStateMsg{
		ActiveTab: "t1",
		Tabs:      []TabInfo{{ID: "t1", Name: "AI", Panes: []string{"pane-m1"}}},
		Panes:     []PaneInfo{{ID: "pane-m1", TabID: "t1", Type: "claude-code"}},
	}
	m.applyWorkspaceState(state)
	if m.tabs[0].Leaves()[0].WideCanvas {
		t.Fatal("setup: pane must start non-canvas before migration")
	}

	// Migration reloads the registry with wide_canvas = true, then the next
	// broadcast re-reconciles the same tab/pane (resync-in-tree branch).
	m.pluginRegistry = flaggedCanvasRegistry(t)
	m.applyWorkspaceState(state)
	m.resizeTabs()

	pane := m.tabs[0].Leaves()[0]
	if !pane.WideCanvas {
		t.Error("resync-in-tree branch did not pick up the post-migration flag flip")
	}
	if w := pane.vt.Width(); w != 207 {
		t.Errorf("post-flip canvas pane VT width %d, want 207", w)
	}
}
