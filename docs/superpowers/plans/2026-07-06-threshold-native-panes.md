# Threshold-native AI panes + preview selection — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make wide-canvas AI panes render natively (real pane-width PTY) once they are wide enough to read, keep the window-sized canvas + preview for narrow panes, and make mouse selection work in narrow preview panes.

**Architecture:** A single threshold (`min_native_cols`, default 80) decides per-pane whether a `wide_canvas` pane uses the window canvas or its own rect. The decision lives only in `paneVTSize`; because `previewMode()` derives from `innerW < vt.Width()`, native panes auto-report `previewMode()==false` and re-enable selection for free. For panes that stay below the threshold, a `previewPosAt` inverse-map (built on the existing `previewLayout.locate`) wires mouse coords through the wrapped-preview layout.

**Tech Stack:** Go 1.25, Bubble Tea v2 / Lipgloss v2 TUI, `charmbracelet/x/ultraviolet` VT. Tests: stdlib `testing`, table-driven. Build/test via Docker: `./scripts/dev.sh {test,test-race,vet}` (host has no Go/make).

**Design doc:** `docs/superpowers/specs/2026-07-06-threshold-native-panes-design.md`

## Global Constraints

- Go: `MixedCaps`, tabs for indentation, `gofmt` mandatory. Acronyms uppercase (`VT`, `PTY`).
- No AI-attribution in commit messages. Imperative mood, ≤72-char subject.
- Commit only files touched by the task, staged by explicit path — never `git add -A` (unrelated WIP may be present).
- Default `min_native_cols` = **80**, applied in code (`0 → 80`). Do **not** edit shipped `claude-code.toml`/`opencode.toml`, do **not** bump `schema_version` (avoids the migration dialog).
- TOML/JSON: 2-space indent. LF endings, final newline.
- Keyboard selection stays out of narrow preview (mouse-only fix).
- All tests must pass under `-race`; run `vet` before final commit.

---

## File Structure

| File | Responsibility | Task |
|------|----------------|------|
| `internal/plugin/plugin.go` | `DisplayConfig.MinNativeCols` field | 1 |
| `internal/plugin/registry.go` | parse `min_native_cols` TOML | 1 |
| `internal/tui/pane.go` | `PaneModel.MinNativeCols` field; pass `sel` to `renderPreview` | 1, 5 |
| `internal/tui/model.go` | `pluginMinNativeCols`; `paneVTSize` call arg; drop preview-select guard; preview coord branch | 1, 2, 4 |
| `internal/tui/workstate.go` | `syncPaneMeta` sets `MinNativeCols` | 1 |
| `internal/tui/canvas.go` | `paneVTSize` threshold rule + `defaultMinNativeCols` | 2 |
| `internal/tui/layout.go` | pass `pane.MinNativeCols` to `paneVTSize` | 2 |
| `internal/tui/tab.go` | pass `pane.MinNativeCols` to `paneVTSize` | 2 |
| `internal/tui/pane_preview.go` | `previewPosAt`; selection render in `renderPreview` | 3, 5 |
| `internal/tui/canvas_test.go` | update `TestPaneVTSize`, focus-toggle test; new threshold test | 1, 2 |
| `internal/tui/pane_preview_test.go` | `previewPosAt` + preview-selection-render tests | 3, 5 |
| `internal/tui/pane_mouse_test.go` | preview mouse-selection wiring test | 4 |
| `docs/plugin-reference.md` | document `[display] min_native_cols` | 6 |
| `.claude/CLAUDE.md` | update wide-canvas note | 6 |

---

## Task 1: `min_native_cols` config plumbing (TOML → `PaneModel.MinNativeCols`)

Carry the threshold from plugin TOML all the way to `PaneModel.MinNativeCols`. Nothing reads it yet (Task 2 does) — this task only proves the value flows.

**Files:**
- Modify: `internal/plugin/plugin.go` (`DisplayConfig`)
- Modify: `internal/plugin/registry.go` (parse struct + copy, ~lines 327, 420)
- Modify: `internal/tui/pane.go` (`PaneModel` struct, near line 48)
- Modify: `internal/tui/model.go` (add `pluginMinNativeCols`, near `pluginWideCanvas` line 1155; update 5 `syncPaneMeta` call sites: 2406, 2447, 2587, 2680, 2691)
- Modify: `internal/tui/workstate.go` (`syncPaneMeta`, line 179)
- Test: `internal/tui/canvas_test.go`

**Interfaces:**
- Produces: `plugin.DisplayConfig.MinNativeCols int`; `PaneModel.MinNativeCols int`; `func (m Model) pluginMinNativeCols(paneType string) int`; `func syncPaneMeta(pane *PaneModel, info *PaneInfo, wideCanvas bool, minNativeCols int)`

- [ ] **Step 1: Write the failing test** — append to `internal/tui/canvas_test.go`:

```go
func TestPluginMinNativeCols(t *testing.T) {
	dir := t.TempDir()
	toml := "[plugin]\nname = \"claude-code\"\nschema_version = 7\n" +
		"[command]\ncmd = \"true\"\n[display]\nwide_canvas = true\nmin_native_cols = 100\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	m := Model{pluginRegistry: reg}
	if got := m.pluginMinNativeCols("claude-code"); got != 100 {
		t.Errorf("pluginMinNativeCols(claude-code) = %d, want 100", got)
	}
	if got := m.pluginMinNativeCols("unknown"); got != 0 {
		t.Errorf("pluginMinNativeCols(unknown) = %d, want 0", got)
	}
}

func TestSyncPaneMeta_SetsMinNativeCols(t *testing.T) {
	dir := t.TempDir()
	toml := "[plugin]\nname = \"claude-code\"\nschema_version = 7\n" +
		"[command]\ncmd = \"true\"\n[display]\nwide_canvas = true\nmin_native_cols = 100\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
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
		Tabs:      []TabInfo{{ID: "t1", Name: "AI", Panes: []string{"p1"}}},
		Panes:     []PaneInfo{{ID: "p1", TabID: "t1", Type: "claude-code"}},
	}
	m.applyWorkspaceState(state)
	if got := m.tabs[0].Leaves()[0].MinNativeCols; got != 100 {
		t.Errorf("pane MinNativeCols = %d, want 100", got)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `./scripts/dev.sh test 2>&1 | grep -E "pluginMinNativeCols|MinNativeCols|undefined|FAIL"`
Expected: compile error `m.pluginMinNativeCols undefined` / `MinNativeCols` field unknown.

- [ ] **Step 3: Add the plugin config field** — `internal/plugin/plugin.go`, inside `DisplayConfig` (after `WideCanvas bool`):

```go
	// MinNativeCols is the inner-width threshold (columns) at or above which
	// a wide_canvas pane renders natively (real pane-width PTY) instead of
	// the window canvas + preview. 0 means "use the built-in default" (80).
	MinNativeCols int
```

- [ ] **Step 4: Parse the TOML key** — `internal/plugin/registry.go`. In the `Display` parse struct (near line 330) add:

```go
		MinNativeCols int    `toml:"min_native_cols"`
```

and in the `DisplayConfig{...}` copy (near line 423, after `WideCanvas:`):

```go
			MinNativeCols: tp.Display.MinNativeCols,
```

- [ ] **Step 5: Add the PaneModel field** — `internal/tui/pane.go`, after the `WideCanvas` field (line 48):

```go
	MinNativeCols      int    // [display] min_native_cols: inner-width threshold for native (non-canvas) rendering; 0 = default 80
```

- [ ] **Step 6: Add the resolver** — `internal/tui/model.go`, directly after `pluginWideCanvas` (ends ~line 1163):

```go
// pluginMinNativeCols resolves the native-rendering column threshold for a
// pane type via the plugin registry. Unknown types (registry miss, nil
// registry in tests) return 0, which paneVTSize treats as the default (80).
func (m Model) pluginMinNativeCols(paneType string) int {
	if m.pluginRegistry == nil {
		return 0
	}
	if p := m.pluginRegistry.Get(paneType); p != nil {
		return p.Display.MinNativeCols
	}
	return 0
}
```

- [ ] **Step 7: Extend `syncPaneMeta`** — `internal/tui/workstate.go` line 179, change signature and add the assignment:

```go
func syncPaneMeta(pane *PaneModel, info *PaneInfo, wideCanvas bool, minNativeCols int) {
	pane.Name = info.Name
	pane.CWD = info.CWD
	pane.Type = info.Type
	pane.WideCanvas = wideCanvas
	pane.MinNativeCols = minNativeCols
	// ... (rest unchanged)
```

- [ ] **Step 8: Update the 5 call sites** — `internal/tui/model.go` lines 2406, 2447, 2587, 2680, 2691. Each becomes (adjust the info variable name per site — `info` / `overlayInfo`):

```go
syncPaneMeta(leaf.Pane, info, m.pluginWideCanvas(info.Type), m.pluginMinNativeCols(info.Type))
```
```go
syncPaneMeta(pane, info, m.pluginWideCanvas(info.Type), m.pluginMinNativeCols(info.Type))
```
```go
syncPaneMeta(pane, overlayInfo, m.pluginWideCanvas(overlayInfo.Type), m.pluginMinNativeCols(overlayInfo.Type))
```
```go
syncPaneMeta(tab.overlayPane, overlayInfo, m.pluginWideCanvas(overlayInfo.Type), m.pluginMinNativeCols(overlayInfo.Type))
```

- [ ] **Step 9: Run tests — verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -E "MinNativeCols|ok  |FAIL"`
Expected: `TestPluginMinNativeCols` and `TestSyncPaneMeta_SetsMinNativeCols` PASS; package builds.

- [ ] **Step 10: Commit**

```bash
git add internal/plugin/plugin.go internal/plugin/registry.go internal/tui/pane.go internal/tui/model.go internal/tui/workstate.go internal/tui/canvas_test.go
git commit -m "feat(tui): plumb min_native_cols from plugin TOML to PaneModel"
```

---

## Task 2: `paneVTSize` threshold — native vs canvas sizing

Make a `wide_canvas` pane use the window canvas **only** when its rect is too narrow (`rectW-2 < min_native_cols`); otherwise use its own rect (native). Thread the new argument through all three callers, and update the existing tests that encode the old always-canvas behavior.

**Files:**
- Modify: `internal/tui/canvas.go` (`paneVTSize`, lines 11-31)
- Modify: `internal/tui/layout.go:423`
- Modify: `internal/tui/model.go:4069`
- Modify: `internal/tui/tab.go:313`
- Test: `internal/tui/canvas_test.go` (rewrite `TestPaneVTSize`, fix focus-toggle test, add threshold test, fix the 2-pane propagation test)

**Interfaces:**
- Consumes: `PaneModel.MinNativeCols` (Task 1).
- Produces: `func paneVTSize(wideCanvas bool, minNativeCols, rectW, rectH, canvasW, canvasH int) (cols, rows int)`; `const defaultMinNativeCols = 80`.

- [ ] **Step 1: Rewrite the failing test** — replace `TestPaneVTSize` in `internal/tui/canvas_test.go` (lines 12-33) with:

```go
func TestPaneVTSize(t *testing.T) {
	cases := []struct {
		name                              string
		wide                              bool
		minNativeCols                     int
		rectW, rectH, canW, canH          int
		wantCols, wantRows                int
	}{
		{"normal pane uses rect", false, 80, 60, 20, 200, 50, 58, 18},
		{"wide narrow pane uses canvas", true, 80, 60, 20, 200, 50, 198, 48},
		{"wide pane at threshold goes native", true, 80, 120, 20, 200, 50, 118, 18},
		{"minNativeCols<=0 defaults to 80", true, 0, 60, 20, 200, 50, 198, 48},
		{"wide canvas degenerate clamps", true, 80, 60, 20, 1, 1, 1, 1},
		{"normal degenerate clamps", false, 80, 2, 2, 200, 50, 1, 1},
		{"zero canvas falls back to rect", true, 80, 60, 20, 0, 0, 58, 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := paneVTSize(tc.wide, tc.minNativeCols, tc.rectW, tc.rectH, tc.canW, tc.canH)
			if c != tc.wantCols || r != tc.wantRows {
				t.Errorf("got %dx%d, want %dx%d", c, r, tc.wantCols, tc.wantRows)
			}
		})
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `./scripts/dev.sh test 2>&1 | grep -E "paneVTSize|not enough arguments|FAIL"`
Expected: compile error — `paneVTSize` called with 6 args but defined with 5.

- [ ] **Step 3: Implement the threshold in `paneVTSize`** — `internal/tui/canvas.go`, replace the function (keep the doc comment, extend it):

```go
const defaultMinNativeCols = 80

func paneVTSize(wideCanvas bool, minNativeCols, rectW, rectH, canvasW, canvasH int) (cols, rows int) {
	if minNativeCols <= 0 {
		minNativeCols = defaultMinNativeCols
	}
	w, h := rectW, rectH
	// Use the window canvas ONLY when the rect is too narrow to render the
	// AI transcript readably; at or above the threshold the pane renders
	// natively at its own width (and resizes like a normal pane).
	if wideCanvas && canvasW > 0 && canvasH > 0 && rectW-2 < minNativeCols {
		w, h = canvasW, canvasH
	}
	cols, rows = w-2, h-2
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}
```

- [ ] **Step 4: Update the three callers** — pass `pane.MinNativeCols` as the new second argument:

`internal/tui/layout.go:423`:
```go
		n.Pane.ResizeVT(paneVTSize(n.Pane.WideCanvas, n.Pane.MinNativeCols, w, h, canvasW, canvasH))
```
`internal/tui/model.go:4069`:
```go
				cols, rows := paneVTSize(pane.WideCanvas, pane.MinNativeCols, pane.Width, pane.Height, tab.CanvasW, tab.CanvasH)
```
`internal/tui/tab.go:313`:
```go
	p.ResizeVT(paneVTSize(p.WideCanvas, p.MinNativeCols, w, h, t.CanvasW, t.CanvasH))
```

- [ ] **Step 5: Fix the focus-toggle invariant test** — in `internal/tui/canvas_test.go`, `TestTabResize_CanvasPane_FocusToggleKeepsVTSize` (lines 38-72), the 2-pane split at 200 now goes native. Narrow the window so pane `a` stays sub-threshold (canvas) and the "zoom ≠ resize" invariant still holds. Change the three `SetCanvas`/`Resize` calls from `200, 50` to `120, 50`, and `wantW, wantH := 198, 48` to `118, 48`. Update the top doc comment's first line to:

```go
// Zoom must not resize a SUB-THRESHOLD canvas pane: with a 120-wide window a
// 2-pane split leaves each rect ~58 inner cols (< 80), so pane `a` stays on
// the window canvas; the grid and focus-mode resizes must produce the same
// canvas-derived VT size.
```

- [ ] **Step 6: Fix the 2-pane propagation test to assert native** — in `TestApplyWorkspaceState_*CanvasFlag` (the 2-pane one, lines ~99-138), the split panes are now native at width 209. Replace the per-leaf size assertion (lines 128-137) with:

```go
	for _, p := range leaves {
		if !p.WideCanvas {
			t.Errorf("pane %s: WideCanvas=false after reconciliation with flagged registry", p.ID)
		}
		// 209-wide window, 2-pane split → each rect ≳ 100 inner cols ≥ 80 →
		// native (rect-sized), narrower than the 207-col canvas, and NOT in
		// preview mode (so selection works).
		if p.previewMode() {
			t.Errorf("pane %s: previewMode=true, want native (rect ≥ threshold)", p.ID)
		}
		if p.vt.Width() >= 207 {
			t.Errorf("pane %s VT width %d, want native (< 207 canvas width)", p.ID, p.vt.Width())
		}
	}
```

- [ ] **Step 7: Add a threshold test (native vs canvas by window width)** — append to `internal/tui/canvas_test.go`:

```go
// A wide window puts each split pane over the threshold → native (previewMode
// false); a narrow window puts them under → canvas (previewMode true).
func TestApplyWorkspaceState_ThresholdSelectsNativeOrCanvas(t *testing.T) {
	newModel := func(w, h int) Model {
		m := Model{
			cfg:            config.Default(),
			notifications:  NewNotificationCenter(30, 50),
			pluginRegistry: flaggedCanvasRegistry(t),
			mcpHighlights:  make(map[string]bool),
			attached:       true,
			width:          w,
			height:         h,
		}
		state := WorkspaceStateMsg{
			ActiveTab: "t1",
			Tabs:      []TabInfo{{ID: "t1", Name: "AI", Panes: []string{"p1", "p2"}}},
			Panes: []PaneInfo{
				{ID: "p1", TabID: "t1", Type: "claude-code"},
				{ID: "p2", TabID: "t1", Type: "claude-code"},
			},
		}
		m.applyWorkspaceState(state)
		m.resizeTabs()
		return m
	}

	wide := newModel(209, 58)
	for _, p := range wide.tabs[0].Leaves() {
		if p.previewMode() {
			t.Errorf("wide window: pane %s in preview, want native", p.ID)
		}
	}

	narrow := newModel(120, 40)
	for _, p := range narrow.tabs[0].Leaves() {
		if !p.previewMode() {
			t.Errorf("narrow window: pane %s native, want preview (rect %d < threshold)", p.ID, p.Width-2)
		}
	}
}
```

- [ ] **Step 8: Run tests — verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -E "canvas|PaneVTSize|Threshold|FocusToggle|ok  |FAIL"`
Expected: all `internal/tui` canvas tests PASS. If the narrow-window case unexpectedly reports native, log `p.Width-2` vs 80 and adjust the window (120 → smaller) — the intent is rect inner < 80.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/canvas.go internal/tui/layout.go internal/tui/model.go internal/tui/tab.go internal/tui/canvas_test.go
git commit -m "feat(tui): render wide-canvas panes natively above min_native_cols"
```

---

## Task 3: `previewPosAt` inverse coordinate map

Add the pane-local `(relX, relY)` → emulator `(col, absLine)` mapping for preview panes, built on the existing `previewLayout.locate`.

**Files:**
- Modify: `internal/tui/pane_preview.go` (new method, place after `renderPreview`)
- Test: `internal/tui/pane_preview_test.go`

**Interfaces:**
- Consumes: `previewLayoutFor`, `previewLayout.locate`, `previewLayout.totalVisual`, `lineContentEnd` (all existing).
- Produces: `func (p *PaneModel) previewPosAt(relX, relY int) (col, absLine int, ok bool)`.

- [ ] **Step 1: Write the failing test** — append to `internal/tui/pane_preview_test.go`:

```go
func TestPreviewPosAt_CropRoundTrip(t *testing.T) {
	// 100-wide emulator, one 95-char row + a short row. Crop mode (default):
	// one visual row per absolute row, viewport bottom-anchored.
	p := canvasPane(t, 100, 6, strings.Repeat("a", 95)+"\r\nshort\r\n")
	defer p.Dispose()
	p.Width = 42  // innerW 40
	p.Height = 6  // innerH 4
	if !p.previewMode() {
		t.Fatalf("setup: want preview mode (innerW 40 < vt %d)", p.vt.Width())
	}
	// Column within the crop window maps 1:1 (seg.start == 0).
	col, _, ok := p.previewPosAt(10, 0)
	if !ok || col != 10 {
		t.Errorf("relX 10 -> col %d ok=%v, want col 10", col, ok)
	}
	// relY past the rendered content is out of range.
	if _, _, ok := p.previewPosAt(0, 99); ok {
		t.Errorf("relY 99 should be out of range")
	}
	// A click past the row's content clamps to content end (95), not off-grid.
	col, _, ok = p.previewPosAt(39, 0)
	if !ok || col > 95 {
		t.Errorf("relX 39 -> col %d ok=%v, want <=95", col, ok)
	}
}

func TestPreviewPosAt_WrapSegments(t *testing.T) {
	// Soft-wrap: a 95-wide row at innerW 40 becomes 3 visual rows
	// [0,40),[40,80),[80,95). The 2nd visual row's relX 5 maps to col 45.
	p := canvasPane(t, 100, 6, strings.Repeat("b", 95)+"\r\n")
	defer p.Dispose()
	p.previewWrap = true
	p.Width = 42 // innerW 40
	p.Height = 8 // innerH 6 — tall enough that all 3 segments are visible
	l := p.previewLayoutFor(40)
	// Find the visual row index of the row's 2nd segment (absolute row 0).
	vSecond := l.prefix[0] + 1
	total := l.totalVisual()
	viewStart := total - 6 // innerH
	if viewStart < 0 {
		viewStart = 0
	}
	relY := vSecond - viewStart
	col, absLine, ok := p.previewPosAt(5, relY)
	if !ok || col != 45 || absLine != 0 {
		t.Errorf("wrap seg2 relX 5 -> col %d line %d ok=%v, want col 45 line 0", col, absLine, ok)
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `./scripts/dev.sh test 2>&1 | grep -E "previewPosAt|undefined|FAIL"`
Expected: `p.previewPosAt undefined`.

- [ ] **Step 3: Implement `previewPosAt`** — `internal/tui/pane_preview.go`, after `renderPreview`:

```go
// previewPosAt maps a pane-local (relX, relY) — already border-adjusted, i.e.
// 0-based within the inner content area — to an emulator (col, absLine) via
// the visual→absolute preview layout. ok is false when the point lands
// outside the rendered content (e.g. below the last line). The mapping is the
// inverse of renderPreview's viewStart + locate() walk, so a click lands on
// the glyph under the cursor in both crop and soft-wrap modes.
func (p *PaneModel) previewPosAt(relX, relY int) (col, absLine int, ok bool) {
	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	l := p.previewLayoutFor(innerW)
	total := l.totalVisual()
	viewStart := total - innerH - p.scrollBack
	if viewStart < 0 {
		viewStart = 0
	}
	v := viewStart + relY
	if v < 0 || v >= total {
		return 0, 0, false
	}
	absRow, s := l.locate(v)
	if relX < 0 {
		relX = 0
	}
	col = s.start + relX
	if col > s.end {
		col = s.end
	}
	// Clamp to the row's real content end so a click in the blank area past
	// text maps to end-of-line rather than an off-grid column.
	if end := lineContentEnd(p, absRow); end >= 0 && col > end+1 {
		col = end + 1
	}
	return col, absRow, true
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `./scripts/dev.sh test 2>&1 | grep -E "PreviewPosAt|ok  |FAIL"`
Expected: `TestPreviewPosAt_CropRoundTrip` and `TestPreviewPosAt_WrapSegments` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/pane_preview.go internal/tui/pane_preview_test.go
git commit -m "feat(tui): add previewPosAt inverse map for wide-canvas previews"
```

---

## Task 4: Mouse selection in preview panes

Arm drag-selection on preview panes and route their coords through `previewPosAt`.

**Files:**
- Modify: `internal/tui/model.go` (drop `armSelection=false` preview guard ~line 615-621; preview branch in `updateMouseSelection` ~line 3690-3762)
- Test: `internal/tui/pane_mouse_test.go`

**Interfaces:**
- Consumes: `previewPosAt` (Task 3); `PaneModel.previewMode`; `TabModel.Root.FindPaneRectAt`.

- [ ] **Step 1: Write the failing test** — append to `internal/tui/pane_mouse_test.go` (add `"strings"` and `"github.com/artyomsv/quil/internal/config"` to imports if missing):

```go
// A click+drag inside a narrow preview pane must build a selection whose
// anchors are mapped through the wrapped-preview layout (previewPosAt), not
// the raw 1:1 screen mapping used for native panes.
func TestUpdateMouseSelection_PreviewPane_RoutesThroughLayout(t *testing.T) {
	m := Model{
		cfg:            config.Default(),
		notifications:  NewNotificationCenter(30, 50),
		pluginRegistry: flaggedCanvasRegistry(t),
		mcpHighlights:  make(map[string]bool),
		attached:       true,
		width:          120,
		height:         40,
	}
	state := WorkspaceStateMsg{
		ActiveTab: "t1",
		Tabs:      []TabInfo{{ID: "t1", Name: "AI", Panes: []string{"p1", "p2"}}},
		Panes: []PaneInfo{
			{ID: "p1", TabID: "t1", Type: "claude-code"},
			{ID: "p2", TabID: "t1", Type: "claude-code"},
		},
	}
	m.applyWorkspaceState(state)
	m.resizeTabs()
	tab := m.tabs[0]
	p := tab.Leaves()[0]
	p.AppendOutput([]byte(strings.Repeat("a", 200) + "\r\nsecond line\r\n"))
	if !p.previewMode() {
		t.Fatalf("setup: pane must be in preview (innerW %d, vt %d)", p.Width-2, p.vt.Width())
	}

	tabH := m.height - chromeHeight
	rect := tab.Root.FindPaneRectAt(0, 1, 0, 1, m.paneAreaWidth(), tabH)
	if rect == nil || rect.Pane != p {
		t.Fatalf("setup: expected first leaf at top-left, got %v", rect)
	}
	// Press at a cell a few columns in, one row down; drag to another cell.
	startX, startY := rect.OX+3, rect.OY+1
	curX, curY := rect.OX+8, rect.OY+2
	m.mouseStartX, m.mouseStartY = startX, startY
	m.updateMouseSelection(tab, curX, curY, tabH)

	if m.selection == nil {
		t.Fatal("preview click+drag produced no selection")
	}
	if m.selection.PaneID != p.ID {
		t.Errorf("selection PaneID = %q, want %q", m.selection.PaneID, p.ID)
	}
	// The anchors must match previewPosAt exactly (proves the branch is wired).
	wantAncCol, wantAncLine, _ := p.previewPosAt(startX-rect.OX-1, startY-rect.OY-1)
	wantCurCol, wantCurLine, _ := p.previewPosAt(curX-rect.OX-1, curY-rect.OY-1)
	if m.selection.Anchor.Col != wantAncCol || m.selection.Anchor.Line != wantAncLine {
		t.Errorf("anchor = (%d,%d), want (%d,%d)", m.selection.Anchor.Col, m.selection.Anchor.Line, wantAncCol, wantAncLine)
	}
	if m.selection.Cursor.Col != wantCurCol || m.selection.Cursor.Line != wantCurLine {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", m.selection.Cursor.Col, m.selection.Cursor.Line, wantCurCol, wantCurLine)
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `./scripts/dev.sh test 2>&1 | grep -E "RoutesThroughLayout|FAIL"`
Expected: FAIL — anchors use the raw screen mapping, not `previewPosAt`.

- [ ] **Step 3: Drop the arming guard** — `internal/tui/model.go`, in `MouseClickMsg` (~lines 614-621). Remove the preview special-case so clicks arm selection on preview panes too. Replace:

```go
				m.clearDragState()
				armSelection := true
				if tab := m.activeTabModel(); tab != nil && !tab.FocusMode() && tab.Root != nil {
					tabH := m.height - chromeHeight
					if pane := tab.Root.FindPaneAt(msg.X, msg.Y, 0, 1, m.paneAreaWidth(), tabH); pane != nil && pane.previewMode() {
						armSelection = false
					}
				}
				if armSelection {
					m.mouseDown = true
					m.mouseStartX = msg.X
					m.mouseStartY = msg.Y
				}
```

with:

```go
				m.clearDragState()
				m.mouseDown = true
				m.mouseStartX = msg.X
				m.mouseStartY = msg.Y
```

- [ ] **Step 4: Add the preview branch in `updateMouseSelection`** — `internal/tui/model.go`. After the block that resolves `pane, ox, oy` and computes `sbLen` (right before `startCol := m.mouseStartX - ox - 1`, ~line 3719), insert a preview short-circuit:

```go
	// Wide-canvas preview panes have no 1:1 grid mapping — the visible rows
	// are wrapped/cropped segments of a wider emulator. Map both endpoints
	// through the layout inverse instead of the raw screen mapping below.
	if pane.previewMode() {
		startCol, startLine, okS := pane.previewPosAt(m.mouseStartX-ox-1, m.mouseStartY-oy-1)
		curCol, curLine, okC := pane.previewPosAt(curX-ox-1, curY-oy-1)
		if !okS && !okC {
			return
		}
		m.selection = &Selection{
			PaneID: pane.ID,
			Anchor: SelectionAnchor{Col: startCol, Line: startLine},
			Cursor: SelectionAnchor{Col: curCol, Line: curLine},
		}
		return
	}

	sbLen := pane.vt.ScrollbackLen()
```

Note: the existing `sbLen := pane.vt.ScrollbackLen()` line that was here moves below the new block (or delete the new duplicate and keep the original — ensure exactly one `sbLen :=` remains before the native mapping). If `previewPosAt` returns `ok=false` for one endpoint (drag past the pane edge), it still clamps to a valid `absRow`/`col` via `locate`, so the anchor stays usable.

- [ ] **Step 5: Run test — verify it passes**

Run: `./scripts/dev.sh test 2>&1 | grep -E "RoutesThroughLayout|ok  |FAIL"`
Expected: `TestUpdateMouseSelection_PreviewPane_RoutesThroughLayout` PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/pane_mouse_test.go
git commit -m "feat(tui): enable mouse selection in wide-canvas preview panes"
```

---

## Task 5: Render the selection highlight in preview

`renderPreview` currently draws only the caret. Draw the selection span too, intersecting each visual row's segment window with the selection's column range.

**Files:**
- Modify: `internal/tui/pane.go` (`renderContent` passes `sel` to `renderPreview`, line 921-923)
- Modify: `internal/tui/pane_preview.go` (`renderPreview` signature + selection intersection)
- Test: `internal/tui/pane_preview_test.go`

**Interfaces:**
- Consumes: `Selection.ColRange(absLine, width int) (startCol, endCol int)`; `styledCellLineWithSelection`.
- Produces: `func (p *PaneModel) renderPreview(sel *Selection) string` (signature change).

- [ ] **Step 1: Write the failing test** — append to `internal/tui/pane_preview_test.go`:

```go
func TestRenderPreview_DrawsSelection(t *testing.T) {
	p := canvasPane(t, 100, 6, strings.Repeat("a", 30)+"\r\n")
	defer p.Dispose()
	p.Active = true
	p.Width = 42 // innerW 40
	p.Height = 6
	if !p.previewMode() {
		t.Fatalf("setup: want preview mode")
	}
	// Select columns 5..10 on absolute row 0 (the 'a' row).
	sel := &Selection{
		PaneID: p.ID,
		Anchor: SelectionAnchor{Col: 5, Line: 0},
		Cursor: SelectionAnchor{Col: 10, Line: 0},
	}
	out := p.renderPreview(sel)
	if !strings.Contains(out, "\x1b[7m") {
		t.Errorf("preview render missing reverse-video selection SGR; got:\n%q", out)
	}
}

func TestRenderPreview_NoSelectionNoReverse(t *testing.T) {
	p := canvasPane(t, 100, 6, strings.Repeat("a", 30)+"\r\n")
	defer p.Dispose()
	p.Width = 42
	p.Height = 6
	// Inactive, no selection, no caret → no reverse-video anywhere.
	out := p.renderPreview(nil)
	if strings.Contains(out, "\x1b[7m") {
		t.Errorf("preview render has reverse-video with no selection/caret; got:\n%q", out)
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `./scripts/dev.sh test 2>&1 | grep -E "DrawsSelection|NoReverse|too many arguments|FAIL"`
Expected: compile error — `renderPreview` takes no args.

- [ ] **Step 3: Change the call site** — `internal/tui/pane.go` line 921-923, pass `sel`:

```go
	if p.previewMode() {
		return p.renderPreview(sel)
	}
```

- [ ] **Step 4: Add selection to `renderPreview`** — `internal/tui/pane_preview.go`. Change the signature to `func (p *PaneModel) renderPreview(sel *Selection) string`. Inside the per-visual-row loop, where `selStart, selEnd := -1, -1` is currently set for the caret, extend it to also cover the range selection. Replace the caret-only block:

```go
		selStart, selEnd := -1, -1
		if v == cursorVisual {
			selStart, selEnd = cursorCol, cursorCol // reverse-video caret
			if w <= cursorCol {
				w = cursorCol + 1 // caret sits on the blank cell after content
			}
		}
```

with:

```go
		selStart, selEnd := -1, -1
		// Range selection on this pane: intersect the selection's column
		// span for this absolute row with the segment window [s.start,s.end)
		// and translate to local (in-segment) columns.
		if sel != nil && sel.PaneID == p.ID {
			cStart, cEnd := sel.ColRange(absRow, p.vt.Width())
			if cStart >= 0 && cEnd >= cStart {
				lo := cStart
				if lo < s.start {
					lo = s.start
				}
				hi := cEnd
				if hi > s.end-1 {
					hi = s.end - 1
				}
				if lo <= hi {
					selStart, selEnd = lo-s.start, hi-s.start
				}
			}
		}
		// Caret (live view) takes precedence as a 1-cell reverse span.
		if v == cursorVisual {
			selStart, selEnd = cursorCol, cursorCol
			if w <= cursorCol {
				w = cursorCol + 1
			}
		}
```

- [ ] **Step 5: Run tests — verify they pass**

Run: `./scripts/dev.sh test 2>&1 | grep -E "DrawsSelection|NoReverse|ok  |FAIL"`
Expected: both new tests PASS; `internal/tui` builds (the only `renderPreview` caller was updated in Step 3).

- [ ] **Step 6: Run the full preview + selection suites under race**

Run: `./scripts/dev.sh test-race 2>&1 | grep -E "internal/tui|FAIL|ok  "`
Expected: `internal/tui` PASS under `-race`.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/pane.go internal/tui/pane_preview.go internal/tui/pane_preview_test.go
git commit -m "feat(tui): render selection highlight in wide-canvas previews"
```

---

## Task 6: Documentation

Document the new knob and update the architecture note. No tests.

**Files:**
- Modify: `docs/plugin-reference.md`
- Modify: `.claude/CLAUDE.md`

- [ ] **Step 1: Document the knob** — in `docs/plugin-reference.md`, under the `[display]` section (next to `wide_canvas`), add:

```markdown
- `min_native_cols` (int, default `80`) — only meaningful with `wide_canvas = true`.
  Inner-width threshold in columns: when a pane's usable width is **≥** this value it
  renders **natively** (its PTY is sized to the pane, Claude wraps at the real width,
  text selection works directly). Below it, the pane keeps the window-sized canvas and
  renders a cropped/soft-wrapped preview. Lower it to favor native rendering in tighter
  splits; raise it to keep more splits on the preview. `0` uses the built-in default (80).
```

- [ ] **Step 2: Update the architecture note** — in `.claude/CLAUDE.md`, in the Notification-center bullet's wide-canvas sentence (the one describing `wide_canvas` / `pane_preview.go`), append:

```markdown
 A wide_canvas pane renders **natively** (PTY sized to its rect) once its inner width ≥ `[display] min_native_cols` (default 80, `paneVTSize` in `canvas.go` is the sole decision point); below the threshold it uses the window canvas + preview. Because `previewMode()` derives from `innerW < vt.Width()`, native panes report `previewMode()==false` and get mouse+keyboard selection for free; narrow preview panes get **mouse** selection via `previewPosAt` (inverse of the `locate` layout) with the highlight drawn in `renderPreview`. Keyboard selection stays native/zoom-only.
```

- [ ] **Step 3: Final verification** — full suite, race, vet:

Run: `./scripts/dev.sh test && ./scripts/dev.sh test-race && ./scripts/dev.sh vet`
Expected: all green across all packages.

- [ ] **Step 4: Commit**

```bash
git add docs/plugin-reference.md .claude/CLAUDE.md
git commit -m "docs: document min_native_cols and native-preview selection"
```

---

## Self-Review

**Spec coverage:**
- Spec §1 (threshold sizing in `paneVTSize`) → Task 2. ✓
- Spec §2 (config: field, registry, PaneModel, resolver, syncPaneMeta, no TOML/schema bump) → Task 1 + Task 6 docs. ✓
- Spec §3a (`previewPosAt`) → Task 3. ✓
- Spec §3b (mouse handlers: drop guard, preview coord branch) → Task 4. ✓
- Spec §3c (selection render in `renderPreview`) → Task 5. ✓
- Spec §3d (keyboard selection stays out) → not touched (guard at `model.go:3792` left intact); noted in Task 6 doc. ✓
- Spec §4 (tests: paneVTSize table, previewPosAt round-trip, preview selection render, propagation) → Tasks 1-5. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code; test code is concrete.

**Type consistency:**
- `paneVTSize(wideCanvas bool, minNativeCols, rectW, rectH, canvasW, canvasH int)` — defined Task 2 Step 3, called Task 2 Step 4 (3 sites) with matching arg order.
- `syncPaneMeta(pane, info, wideCanvas, minNativeCols)` — defined Task 1 Step 7, called Task 1 Step 8 (5 sites).
- `pluginMinNativeCols(paneType string) int` — defined Task 1 Step 6, used Task 1 Step 8.
- `previewPosAt(relX, relY int) (col, absLine int, ok bool)` — defined Task 3, used Task 4 Step 4 + tested Task 4 Step 1.
- `renderPreview(sel *Selection) string` — signature changed Task 5 Step 4, sole caller updated Task 5 Step 3.
- `DisplayConfig.MinNativeCols` / `PaneModel.MinNativeCols` — Task 1.

**Open risk to watch during execution:** the narrow-window threshold in Task 2 Step 7 and Task 4 (window 120 → expect preview) depends on the exact split arithmetic; if a leaf reports `Width-2 ≥ 80` there, shrink the test window until `previewMode()` is true (the assertion messages print `Width-2` to guide this).
