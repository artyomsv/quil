# Wide Canvas Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AI panes (claude-code, opencode) keep a window-sized PTY/emulator forever; small grid panes render a soft-wrapped view of that wide buffer, so no user action ever resizes an AI pane's terminal.

**Architecture:** Per `docs/superpowers/specs/2026-07-05-wide-canvas-design.md`. `paneVTSize` becomes the single sizing authority (canvas panes → tab-area size; others → rect size). A preview layer in `pane_preview.go` wraps wide emulator rows into visual rows at the pane's inner width (cached by `(contentGen, innerW)`), with bottom anchoring, visual-row scrolling, and a mapped cursor. Fix #3 from the previous branch phase is reverted first.

**Tech Stack:** Go 1.25, charmbracelet/x/vt SafeEmulator (`CellAt`/`ScrollbackCellAt`/`CursorPosition`), existing cell walkers `styledCellLine`/`styledCellLineWithSelection`.

## Global Constraints

- Branch: `worktree-resize-artifacts` (same worktree at `.claude/worktrees/resize-artifacts`).
- Build/test: `./scripts/dev.sh test` (Docker). `dev.sh build` is broken by the pre-existing CRLF issue — for binaries use the direct docker build command from the session notes.
- Commits: imperative, ≤72 chars, no AI attribution. Never commit `docs/superpowers/`.
- Go: tabs, gofmt, table-driven tests, `TestFunc_Scenario_Expected` naming.

---

### Task 1: Revert fix #3 (clean-screen-on-width-change)

**Files:**
- Revert commit `d1c3a14` (touches `internal/tui/pane.go`, deletes `internal/tui/pane_resize_clear_test.go`)
- Modify: `.claude/CLAUDE.md` (drop the `clearsOnWidthResize`/`pushScreenToScrollback` sentence added in `545623b`)

- [ ] **Step 1:** `git revert --no-edit d1c3a14` — clean revert expected (later commits didn't touch `ResizeVT`).
- [ ] **Step 2:** Edit `.claude/CLAUDE.md`: in the notification-center bullet, replace the trailing sentence `and `ResizeVT` pushes a mainscreen agent pane's screen into scrollback on width change (`clearsOnWidthResize`/`pushScreenToScrollback` in pane.go) so the child's post-resize repaint lands on a blank screen` with `and AI panes render on a window-sized canvas (`wide_canvas`, see internal/tui/pane_preview.go) so grid/zoom/sidebar changes never resize their PTY`. Amend the revert commit with this doc line: `git add .claude/CLAUDE.md && git commit --amend --no-edit`.
- [ ] **Step 3:** Run `./scripts/dev.sh test` — full suite green (pane_resize_clear tests gone).

### Task 2: `wide_canvas` plugin flag

**Files:**
- Modify: `internal/plugin/plugin.go` (DisplayConfig struct)
- Modify: `internal/plugin/registry.go:326-331` (tomlPlugin.Display), `:419-422` (conversion)
- Modify: `internal/plugin/defaults/claude-code.toml` (`schema_version = 7`, add `[display]` entry `wide_canvas = true`)
- Modify: `internal/plugin/defaults/opencode.toml` (`schema_version = 2`, same)
- Test: `internal/plugin/wide_canvas_test.go` (new)

**Interfaces:**
- Produces: `DisplayConfig.WideCanvas bool` (TOML `[display] wide_canvas`, default false). Task 3 consumes via `registry.Get(paneType).Display.WideCanvas`.

- [ ] **Step 1: Failing test**

```go
package plugin

import "testing"

func TestLoadTOML_WideCanvasFlag(t *testing.T) {
	data := []byte(`
[plugin]
name = "wc-test"
schema_version = 1
[command]
cmd = "true"
[display]
wide_canvas = true
`)
	p, err := parseTOMLPlugin(data) // use the package's actual parse entry (see registry.go loadFile path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.Display.WideCanvas {
		t.Error("wide_canvas = true not parsed into DisplayConfig.WideCanvas")
	}
}

func TestLoadTOML_WideCanvasDefaultFalse(t *testing.T) {
	data := []byte("[plugin]\nname = \"wc-default\"\nschema_version = 1\n[command]\ncmd = \"true\"\n")
	p, err := parseTOMLPlugin(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Display.WideCanvas {
		t.Error("WideCanvas must default to false")
	}
}
```

If the package exposes no `parseTOMLPlugin`, use the existing test-visible load helper (grep `func.*toml` in `registry.go` and mirror whatever `registry_test.go` uses — write the file via `t.TempDir()` + the registry loader if that's the established pattern).

- [ ] **Step 2:** Run — FAIL (`unknown field WideCanvas`).
- [ ] **Step 3:** Implement: add `WideCanvas bool` to `DisplayConfig` (plugin.go), `WideCanvas bool \`toml:"wide_canvas"\`` to `tomlPlugin.Display` (registry.go), copy in the conversion (`WideCanvas: tp.Display.WideCanvas`). Bump defaults: claude-code.toml `schema_version = 7`, opencode.toml `schema_version = 2`, each gains (or extends) `[display]` with `wide_canvas = true`.
- [ ] **Step 4:** Run tests — PASS (plugin package; also full suite for migration-dialog fixtures that pin schema versions — update any test constants that assert 6/1).
- [ ] **Step 5:** Commit `feat(plugin): add display.wide_canvas flag for AI panes`.

### Task 3: Canvas sizing model

**Files:**
- Create: `internal/tui/canvas.go` + `internal/tui/canvas_test.go`
- Modify: `internal/tui/tab.go` (TabModel fields + `SetCanvas`, `sizePaneFull`), `internal/tui/layout.go:421` (resizeNode), `internal/tui/model.go` (`resizeTabs`, View()'s `tab.Resize` call site, `resizeAllPanes`, pane reconciliation ~2437, pane-creation path)
- Modify: `internal/tui/pane.go` (PaneModel `WideCanvas bool` field + `wideCanvas` in `paneRenderKey`/`renderKey`)

**Interfaces:**
- Produces: `paneVTSize(wideCanvas bool, rectW, rectH, canvasW, canvasH int) (cols, rows int)`; `TabModel.CanvasW/CanvasH int` + `(t *TabModel) SetCanvas(w, h int)`; `PaneModel.WideCanvas bool`.
- Consumes: `DisplayConfig.WideCanvas` from Task 2 via `m.pluginRegistry.Get(info.Type)`.

- [ ] **Step 1: Failing tests** (`canvas_test.go`)

```go
package tui

import "testing"

func TestPaneVTSize(t *testing.T) {
	cases := []struct {
		name                       string
		wide                       bool
		rectW, rectH, canW, canH   int
		wantCols, wantRows         int
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

// Zoom must not resize a canvas pane: sizePaneFull and the grid resize must
// agree on the same canvas dimensions.
func TestTabResize_CanvasPane_FocusToggleKeepsVTSize(t *testing.T) {
	tab := NewTabModel("t", "T")
	a := NewPaneModel("a", 4096)
	a.WideCanvas = true
	b := NewPaneModel("b", 4096)
	tab.Root = &LayoutNode{
		SplitDir: SplitHorizontal,
		Left:     &LayoutNode{Pane: a},
		Right:    &LayoutNode{Pane: b},
	}
	tab.ActivePane = "a"
	tab.SetCanvas(200, 50)
	tab.Resize(200, 50)
	wantW, wantH := 198, 48
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Fatalf("grid: canvas pane VT %dx%d, want %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}
	tab.ToggleFocus()
	tab.Resize(200, 50)
	if a.vt.Width() != wantW || a.vt.Height() != wantH {
		t.Errorf("focus: canvas pane VT %dx%d, want unchanged %dx%d", a.vt.Width(), a.vt.Height(), wantW, wantH)
	}
	// Non-canvas sibling keeps rect-based size in grid mode.
	tab.ExitFocus()
	tab.Resize(200, 50)
	if b.vt.Width() >= wantW {
		t.Errorf("non-canvas pane VT width %d must track its rect, not the canvas", b.vt.Width())
	}
}
```

(Adjust the LayoutNode literal to the package's real constructor — see `layout_test.go` for the established split-building helper and reuse it.)

- [ ] **Step 2:** Run — FAIL (`undefined: paneVTSize`, `SetCanvas`, `WideCanvas`).
- [ ] **Step 3: Implement**

`internal/tui/canvas.go`:

```go
package tui

// paneVTSize is the single authority for a pane's terminal grid size.
// Non-canvas panes get their layout rect minus the border. Wide-canvas
// panes (claude-code, opencode via [display] wide_canvas) always get the
// full tab-area canvas minus the border, regardless of their rect — the
// pane's PTY therefore never resizes when the grid, sidebar, notes panel,
// or focus mode change; the preview renderer (pane_preview.go) adapts the
// wide buffer to small rects instead. Zero canvas (tests, pre-first-resize)
// falls back to the rect.
func paneVTSize(wideCanvas bool, rectW, rectH, canvasW, canvasH int) (cols, rows int) {
	w, h := rectW, rectH
	if wideCanvas && canvasW > 0 && canvasH > 0 {
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

`tab.go`: add `CanvasW, CanvasH int` fields to TabModel; add

```go
// SetCanvas records the full tab-area dimensions used to size wide-canvas
// panes. Callers (resizeTabs, View) set it BEFORE Resize so the canvas is
// independent of notes-panel squeeze: canvas = (window width, tab height).
func (t *TabModel) SetCanvas(w, h int) { t.CanvasW, t.CanvasH = w, h }
```

`sizePaneFull` (tab.go:305) becomes:

```go
func sizePaneFull(t *TabModel, p *PaneModel, w, h int) {
	p.Width = w
	p.Height = h
	p.ResizeVT(paneVTSize(p.WideCanvas, w, h, t.CanvasW, t.CanvasH))
}
```

(update its two call sites in `TabModel.Resize` to pass `t`). `resizeNode` (layout.go:421): change signature to `resizeNode(n *LayoutNode, x, y, w, h, canvasW, canvasH int)` — keep the existing rect math, replace `n.Pane.ResizeVT(w-2, h-2)` with `n.Pane.ResizeVT(paneVTSize(n.Pane.WideCanvas, w, h, canvasW, canvasH))`; `TabModel.Resize` passes `t.CanvasW, t.CanvasH` down every recursive call.

`model.go`:
- `resizeTabs()`: before each `tab.Resize`, `tab.SetCanvas(m.width, tabH)`.
- View() (~1547): `tab.SetCanvas(m.width, tabH)` before `tab.Resize(m.width-notesW, tabH)`.
- `resizeAllPanes()` (~3939): for each leaf compute `cols, rows := paneVTSize(pane.WideCanvas, pane.Width, pane.Height, tab.CanvasW, tab.CanvasH)` instead of the inline `pane.Width-2` math.
- Reconciliation (~2437) and any pane-creation path that sets `pane.Type`: also set `pane.WideCanvas = m.pluginWideCanvas(info.Type)` with

```go
// pluginWideCanvas resolves the wide-canvas flag for a pane type. Unknown
// types (registry miss) render 1:1.
func (m Model) pluginWideCanvas(paneType string) bool {
	if m.pluginRegistry == nil {
		return false
	}
	if p := m.pluginRegistry.Get(paneType); p != nil {
		return p.Display.WideCanvas
	}
	return false
}
```

`pane.go`: add `WideCanvas bool` to PaneModel (daemon-independent, set TUI-side); add `wideCanvas bool` to `paneRenderKey` and `renderKey()`.

- [ ] **Step 4:** Run full suite — PASS. Existing layout tests keep passing because zero canvas falls back to rect sizing.
- [ ] **Step 5:** Commit `feat(tui): size wide-canvas panes to the window, not their rect`.

### Task 4: Preview layout (wrap segmentation + cache)

**Files:**
- Create: `internal/tui/pane_preview.go` + `internal/tui/pane_preview_test.go`

**Interfaces:**
- Produces:
  - `(p *PaneModel) previewMode() bool` — WideCanvas && innerW < vt.Width(), innerW = p.Width-2 clamped ≥1.
  - `type previewLayout struct { innerW int; contentGen uint64; segs [][]seg; prefix []int }` where `seg struct{ start, end int }` is a half-open column window of one absolute row (scrollback rows first, then screen rows).
  - `(p *PaneModel) previewLayoutFor(innerW int) *previewLayout` — cached on PaneModel (`pvCache *previewLayout`), rebuilt when `contentGen` or `innerW` differ.
  - `(l *previewLayout) totalVisual() int`; `(l *previewLayout) visualIndex(absRow, col int) int` (prefix[absRow] + segment index containing col).
- Consumes: `lineContentEnd(p, absLine)` (selection.go) for row content width; `cellAccessor(p, absLine)` for wide-glyph boundary checks.

- [ ] **Step 1: Failing tests** — cover: plain row wider than innerW splits into ceil segments; blank row → exactly 1 empty segment; CJK lead cell straddling a boundary shifts the boundary left by one column; segments cover exactly `[0, contentEnd]`; prefix sums monotone; cache hit (same pointer) for unchanged `(contentGen, innerW)` and rebuild on `AppendOutput`.

```go
package tui

import (
	"strings"
	"testing"
)

func canvasPane(t *testing.T, cols, rows int, feed string) *PaneModel {
	t.Helper()
	p := NewPaneModel("pv", 4096)
	p.Type = "claude-code"
	p.WideCanvas = true
	p.ResizeVT(cols, rows)
	if feed != "" {
		p.AppendOutput([]byte(feed))
	}
	return p
}

func TestPreviewLayout_WrapCounts(t *testing.T) {
	p := canvasPane(t, 100, 8, strings.Repeat("a", 95)+"\r\nshort\r\n\r\n")
	defer p.Dispose()
	p.Width = 42 // innerW 40
	l := p.previewLayoutFor(40)
	// rows: [95 a's]=3 segs, [short]=1, [blank]=1, [cursor row blank]=1 …
	if got := len(l.segs[0]); got != 3 {
		t.Errorf("95-wide row at innerW 40: %d segs, want 3", got)
	}
	if got := len(l.segs[1]); got != 1 {
		t.Errorf("short row: %d segs, want 1", got)
	}
	if got := len(l.segs[2]); got != 1 {
		t.Errorf("blank row: %d segs, want 1", got)
	}
	if l.segs[0][2].start != 80 || l.segs[0][2].end != 95 {
		t.Errorf("third seg = [%d,%d), want [80,95)", l.segs[0][2].start, l.segs[0][2].end)
	}
}

func TestPreviewLayout_WideGlyphBoundary(t *testing.T) {
	// 21 CJK glyphs = 42 cells at innerW 40: glyph 21 (cells 40-41) must not
	// straddle — first segment ends at col 40 exactly here; craft the nasty
	// case with an odd prefix: "x" + 20 glyphs → lead cell at col 39,
	// continuation at 40 → boundary must retreat to 39.
	p := canvasPane(t, 100, 6, "x"+strings.Repeat("你", 20)+"\r\n")
	defer p.Dispose()
	l := p.previewLayoutFor(40)
	first := l.segs[0][0]
	if first.end != 39 {
		t.Errorf("segment boundary %d, want 39 (wide glyph must not straddle)", first.end)
	}
	if l.segs[0][1].start != 39 {
		t.Errorf("second segment starts at %d, want 39", l.segs[0][1].start)
	}
}

func TestPreviewLayout_CacheInvalidation(t *testing.T) {
	p := canvasPane(t, 100, 6, "hello\r\n")
	defer p.Dispose()
	l1 := p.previewLayoutFor(40)
	if p.previewLayoutFor(40) != l1 {
		t.Error("unchanged (contentGen, innerW) must reuse the cached layout")
	}
	p.AppendOutput([]byte("more\r\n"))
	if p.previewLayoutFor(40) == l1 {
		t.Error("AppendOutput must invalidate the preview layout cache")
	}
	if p.previewLayoutFor(30) == p.previewLayoutFor(40) {
		t.Error("different innerW must produce a different layout")
	}
}
```

- [ ] **Step 2:** Run — FAIL (undefined types/methods).
- [ ] **Step 3: Implement** `pane_preview.go`:

```go
package tui

// Preview layer for wide-canvas panes: the emulator is window-sized, the
// pane's rect can be much narrower. previewLayout wraps every absolute row
// (scrollback rows, then screen rows) into column windows ("segments") of
// at most innerW cells, respecting wide-glyph boundaries. Rendering and
// scrolling in preview mode operate on these visual rows. Cached per pane
// keyed on (contentGen, innerW) — one rebuild per output burst, not per
// frame. See docs/superpowers/specs/2026-07-05-wide-canvas-design.md.

type seg struct{ start, end int } // half-open cell-column window of one row

type previewLayout struct {
	innerW     int
	contentGen uint64
	segs       [][]seg // per absolute row (scrollback + screen)
	prefix     []int   // prefix[i] = visual rows before absolute row i
}

func (p *PaneModel) previewMode() bool {
	if !p.WideCanvas {
		return false
	}
	innerW := p.Width - 2
	return innerW >= 1 && innerW < p.vt.Width()
}

// previewLayoutFor returns the wrap layout for innerW, rebuilding only when
// the emulator content or the width changed.
func (p *PaneModel) previewLayoutFor(innerW int) *previewLayout {
	if p.pvCache != nil && p.pvCache.innerW == innerW && p.pvCache.contentGen == p.contentGen {
		return p.pvCache
	}
	sbLen := p.vt.ScrollbackLen()
	h := p.vt.Height()
	total := sbLen + h
	l := &previewLayout{innerW: innerW, contentGen: p.contentGen,
		segs: make([][]seg, total), prefix: make([]int, total+1)}
	for row := 0; row < total; row++ {
		l.segs[row] = wrapRow(cellAccessor(p, row), lineContentEnd(p, row), innerW)
		l.prefix[row+1] = l.prefix[row] + len(l.segs[row])
	}
	p.pvCache = l
	return l
}

func (l *previewLayout) totalVisual() int { return l.prefix[len(l.prefix)-1] }

// visualIndex maps an absolute (row, col) to its visual-row index.
func (l *previewLayout) visualIndex(absRow, col int) int {
	if absRow < 0 || absRow >= len(l.segs) {
		return 0
	}
	v := l.prefix[absRow]
	for i, s := range l.segs[absRow] {
		if col < s.end || i == len(l.segs[absRow])-1 {
			return v + i
		}
	}
	return v
}

// wrapRow splits one wide row into innerW-wide segments over [0, contentEnd].
// A blank row (contentEnd < 0) is a single empty segment. A wide glyph whose
// lead cell sits at a would-be boundary keeps lead+continuation together by
// retreating the boundary one column.
func wrapRow(getCell func(x int) *uv.Cell, contentEnd, innerW int) []seg {
	if innerW < 1 {
		innerW = 1
	}
	if contentEnd < 0 {
		return []seg{{0, 0}}
	}
	var out []seg
	start := 0
	for start <= contentEnd {
		end := start + innerW
		if end > contentEnd+1 {
			end = contentEnd + 1
		} else if c := getCell(end); c != nil && c.Width == 0 {
			end-- // lead glyph would straddle the cut; keep it whole
		}
		if end <= start { // pathological innerW=1 vs wide glyph
			end = start + 1
		}
		out = append(out, seg{start, end})
		start = end
	}
	return out
}
```

Add to PaneModel (pane.go): `pvCache *previewLayout` (below the render-cache block, with a pointer comment: invalidated implicitly by the `(contentGen, innerW)` key). Import `uv "github.com/charmbracelet/ultraviolet"` — copy the exact alias used in selection.go.

- [ ] **Step 4:** Run — PASS (verify the cursor-row expectations in `TestPreviewLayout_WrapCounts`: powershell isn't running here, feed goes straight to the emulator, so rows are exactly the fed lines).
- [ ] **Step 5:** Commit `feat(tui): preview wrap layout for wide-canvas panes`.

### Task 5: Preview renderer

**Files:**
- Modify: `internal/tui/pane_preview.go` (+ renderer), `internal/tui/pane.go` (`renderContent` branch), `internal/tui/pane_preview_test.go`

**Interfaces:**
- Produces: `(p *PaneModel) renderPreview() string` — full inner frame (innerW × innerH), bottom-anchored live view, visual-row scrollback view with the scrollbar column, software cursor at the mapped position.
- Consumes: Task 4's layout; `styledCellLineWithSelection` (cursor cell via selStart==selEnd), `styledCellLine`.

- [ ] **Step 1: Failing tests** — live view shows the wrapped tail (`prompt>` visible at bottom); every rendered line ≤ innerW cells; a 95-char row appears as 3 consecutive lines; cursor cell rendered in reverse video at the wrapped position; scrolled view pins the scrollbar and shows earlier visual rows.

```go
func TestRenderPreview_BottomAnchoredAndWrapped(t *testing.T) {
	p := canvasPane(t, 100, 6, strings.Repeat("w", 95)+"\r\nprompt> ")
	defer p.Dispose()
	p.Width, p.Height = 42, 8 // innerW 40, innerH 6
	out := ansi.Strip(p.renderPreview())
	lines := strings.Split(out, "\n")
	if len(lines) != 6 {
		t.Fatalf("preview height %d, want 6", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "prompt>") {
		t.Errorf("live preview must show the screen tail, got:\n%s", joined)
	}
	if !strings.Contains(joined, strings.Repeat("w", 40)) {
		t.Errorf("wide row must appear wrapped at 40 cols")
	}
}

func TestRenderPreview_LinesFitInnerWidth(t *testing.T) {
	p := canvasPane(t, 120, 6, strings.Repeat("你x", 30)+"\r\n")
	defer p.Dispose()
	p.Width, p.Height = 42, 8
	for i, line := range strings.Split(p.renderPreview(), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("line %d width %d exceeds innerW 40", i, w)
		}
	}
}
```

- [ ] **Step 2:** Run — FAIL (`undefined: renderPreview`).
- [ ] **Step 3: Implement.** In `pane_preview.go`:

```go
// renderPreview renders the wrapped view of a wide-canvas pane.
// scrollBack counts VISUAL rows here (see the preview branches in
// ScrollUp/ScrollDown). Live view (scrollBack == 0) bottom-anchors on the
// end of the layout; the cursor is drawn with reverse video through the
// same segment walker used for selection rendering.
func (p *PaneModel) renderPreview() string {
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
	scrolled := p.scrollBack > 0

	// Cursor position in visual space (live view only).
	cursorVisual, cursorCol := -1, -1
	if !scrolled && p.Active && p.cursorVisible {
		pos := p.vt.CursorPosition()
		absRow := p.vt.ScrollbackLen() + pos.Y
		cursorVisual = l.visualIndex(absRow, pos.X)
		if segs := l.segs[absRow]; len(segs) > 0 {
			s := segs[cursorVisual-l.prefix[absRow]]
			cursorCol = pos.X - s.start
		}
	}

	contentW := innerW
	if scrolled {
		contentW = innerW - 1 // reserve the scrollbar column
	}
	lines := make([]string, innerH)
	for i := 0; i < innerH; i++ {
		v := viewStart + i
		if v < 0 || v >= total {
			lines[i] = ""
			continue
		}
		absRow, s := l.locate(v)
		getCell := cellAccessor(p, absRow)
		window := func(x int) *uv.Cell {
			if s.start+x >= s.end {
				return nil
			}
			return getCell(s.start + x)
		}
		selStart, selEnd := -1, -1
		if v == cursorVisual {
			selStart, selEnd = cursorCol, cursorCol // reverse-video caret
		}
		lines[i] = p.styledCellLineWithSelection(window, min(contentW, s.end-s.start+1), selStart, selEnd)
	}

	if scrolled {
		// Scrollbar over visual rows — same thumb math as renderScrollback.
		thumbSize := max(1, innerH*innerH/max(1, total))
		scrollRange := total - innerH
		thumbPos := 0
		if scrollRange > 0 {
			thumbPos = viewStart * (innerH - thumbSize) / scrollRange
		}
		if thumbPos < 0 {
			thumbPos = 0
		}
		for i, line := range lines {
			ch := "░"
			if i >= thumbPos && i < thumbPos+thumbSize {
				ch = "█"
			}
			lw := ansi.StringWidth(line)
			if lw > contentW {
				line = ansi.Truncate(line, contentW, "")
			} else if lw < contentW {
				line = line + strings.Repeat(" ", contentW-lw)
			}
			lines[i] = line + "\x1b[90m" + ch + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

// locate maps a visual-row index back to (absolute row, segment).
func (l *previewLayout) locate(v int) (absRow int, s seg) {
	lo, hi := 0, len(l.segs)-1
	for lo < hi { // binary search on prefix sums
		mid := (lo + hi + 1) / 2
		if l.prefix[mid] <= v {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	segs := l.segs[lo]
	idx := v - l.prefix[lo]
	if idx < 0 || idx >= len(segs) {
		return lo, seg{0, 0}
	}
	return lo, segs[idx]
}
```

Guard the cursor-col edge: when `cursorVisual` computes, clamp `cursorCol` to `[0, contentW-1]`; when the cursor's absRow has no segments treat as no cursor. `renderContent` (pane.go:773) gains, BEFORE the selection branch:

```go
	// Wide-canvas preview: wrapped view of the window-sized buffer.
	// Selection is zoom-only in preview mode (spec v1), so the selection
	// branch below is intentionally unreachable here.
	if p.previewMode() {
		return p.renderPreview()
	}
```

- [ ] **Step 4:** Run — PASS, full suite green (non-canvas panes: `previewMode()` false everywhere → zero behavior change).
- [ ] **Step 5:** Commit `feat(tui): render wrapped preview for wide-canvas panes`.

### Task 6: Preview scrolling + selection guards

**Files:**
- Modify: `internal/tui/pane.go` (`ScrollUp/ScrollDown/ScrollToRelY` preview branches), `internal/tui/model.go` (mouse-click selection anchor + shift-arrow selection guards)
- Test: extend `internal/tui/pane_preview_test.go`

**Interfaces:**
- Consumes: `previewMode()`, `previewLayoutFor`, `totalVisual()`.

- [ ] **Step 1: Failing tests**

```go
func TestPreviewScroll_ClampsToVisualRows(t *testing.T) {
	var feed strings.Builder
	for i := 0; i < 20; i++ {
		feed.WriteString(strings.Repeat("z", 95) + "\r\n")
	}
	p := canvasPane(t, 100, 6, feed.String())
	defer p.Dispose()
	p.Width, p.Height = 42, 8 // innerW 40 → each row ≈3 visual rows
	l := p.previewLayoutFor(40)
	maxScroll := l.totalVisual() - 6
	p.ScrollUp(1000000)
	if p.scrollBack != maxScroll {
		t.Errorf("scrollBack clamped to %d, want %d (visual rows, not emulator scrollback %d)",
			p.scrollBack, maxScroll, p.vt.ScrollbackLen())
	}
	p.ScrollDown(1000000)
	if p.scrollBack != 0 {
		t.Errorf("ScrollDown floor: %d, want 0", p.scrollBack)
	}
}
```

- [ ] **Step 2:** Run — FAIL (clamp still uses `vt.ScrollbackLen()`).
- [ ] **Step 3: Implement.** `ScrollUp` (pane.go:322): preview branch clamps to `max(0, l.totalVisual() - innerH)`; `ScrollToRelY` maps relY against visual totals (same inverse-thumb contract, `sbLen` replaced by `totalVisual()-innerH`). Model guards: in the MouseClickMsg selection-anchor path and the shift-arrow selection cases, skip when the target pane `previewMode()` (click still sets active pane; keys fall through to PTY input as for any unbound key — mirror how selection already bails on `renderScrollback` edge cases; place the guard where the selection anchor is first created so focus/other click behavior is untouched).
- [ ] **Step 4:** Run full suite — PASS.
- [ ] **Step 5:** Commit `feat(tui): visual-row scrolling and selection guards for canvas previews`.

### Task 7: Docs + full verification

**Files:**
- Modify: `docs/plugin-reference.md` (`[display]` table: `wide_canvas`), `docs/features.md` (canvas note in the AI-panes section), `.claude/CLAUDE.md` (already touched in Task 1 — verify wording), `docs/configuration.md` only if it mentions pane sizing.

- [ ] **Step 1:** `./scripts/dev.sh test && ./scripts/dev.sh test-race && ./scripts/dev.sh vet` — all green.
- [ ] **Step 2:** Build dev binaries (direct docker command from session notes) for user smoke test.
- [ ] **Step 3:** Docs edits; commit `docs: wide canvas plugin flag and preview behavior`.
- [ ] **Step 4:** Hand off to user smoke test: fresh dev daemon (old workspace has non-canvas sizes; panes heal via resizeAllPanes + resizeKick), verify: claude pane in a 4-split renders wrapped preview; typing works; wheel scroll works; Ctrl+E zoom is instant with NO claude repaint burst in `.quil/quil.log` (pane-out ≈ 0 after zoom); window resize still repaints tail (expected).

## Self-Review Notes

- Spec coverage: sizing → Task 3; preview renderer/cache/cursor → Tasks 4-5; scrolling → Task 6; selection guards → Task 6; plugin flag + migration → Task 2; #3 revert → Task 1; docs/limits → Task 7.
- Type consistency: `paneVTSize(wideCanvas bool, rectW, rectH, canvasW, canvasH int)` used identically in Tasks 3; `previewLayoutFor(innerW int) *previewLayout`, `seg{start,end}`, `totalVisual()`, `locate(v)` consistent across Tasks 4-6; `PaneModel.WideCanvas` set in Task 3, read in Tasks 4-6.
- Known judgment calls: exact layout-tree test helper (layout_test.go), the parse entry point name in plugin tests (registry_test.go pattern), min/max helpers (Go 1.21 builtins available on 1.25).
