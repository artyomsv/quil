# Wide Canvas for AI Panes — Design

**Date:** 2026-07-05
**Status:** Approved
**Branch:** worktree-resize-artifacts (continues the resize-artifacts work)

## Problem

Claude-code renders its transcript at the width the pane has while the reply
streams, as immutable hard-wrapped text. On resize it re-renders only its
last screenful (~2.5–9 KB bursts, confirmed in dev logs 2026-07-05), so any
later width change produces mixed-width text on screen and duplicates in
scrollback. Fix #3 from the 2026-07-04 spec (clean-screen-on-width-change)
attacked the symptom and failed user validation: the pushed/cleared screen
still reads as garbled once the user scrolls, and each zoom adds a pushed
copy to scrollback. A probe against the dev daemon also refuted the ConPTY
re-emission theory (zero bytes emitted after a width-only resize of a static
pane), confirming the bursts are claude's own tail re-render.

Root inversion: instead of cleaning up after resizes, make AI panes never
resize. The child always renders at full window width; small grid panes show
a wrapped *view* of that wide buffer.

## Decisions (user-approved)

- Preview transform: **soft-wrap everything** (character wrap, like the notes
  editor) — not crop. Claude's wide chrome lines wrap into multi-row blocks
  in previews; acceptable, previews are for monitoring, zoom is for reading.
- Scope: **plugin TOML flag `wide_canvas = true`**, shipped enabled in the
  claude-code and opencode default plugins (schema_version bump → one-time
  migration dialog). All other pane types stay 1:1.
- V1 interaction in previews: typing, cursor display, and scrolling work;
  **text selection is zoom-only** (mouse click in a preview focuses the pane,
  no selection; shift-arrow selection disabled in preview mode).
- Fix #3 (`clearsOnWidthResize` / `pushScreenToScrollback`) is **reverted** —
  moot once canvas panes stop resizing.

## Design

### 1. Sizing model

For `wide_canvas` panes, PTY and VT emulator are always sized to the full
tab content area — exactly what focus mode yields today: `(tabW-2, tabH-2)`,
clamped ≥ 1×1. A single helper becomes the source of truth for every sizing
path (`resizeNode`, `sizePaneFull`, `Model.resizeAllPanes`):

    // paneVTSize returns the terminal grid size for a pane whose rect is
    // rectW×rectH inside a tab of tabW×tabH.
    paneVTSize(wideCanvas bool, rectW, rectH, tabW, tabH int) (cols, rows int)

- `wideCanvas == false` → `(rectW-2, rectH-2)` (today's behavior).
- `wideCanvas == true`  → `(tabW-2, tabH-2)` regardless of rect.

`resizeAllPanes` sends the same canvas size for every canvas pane, and the
daemon-side same-size guard (fix #2) makes those no-ops. Net effect: splits,
sidebar, notes mode, and focus zoom never resize an AI pane's PTY. Only a
real window resize changes the canvas (claude re-renders its tail once —
same as any terminal emulator).

Daemon: no changes. It receives canvas-sized `resize_pane`, persists canvas
`cols/rows` in workspace.json, and `newRestoredPTY`/`resizeKick` boot
restored panes at canvas size automatically. Fresh spawns start 80×24 and
heal via the existing TUI-resize + `resizeKick` path.

The TUI resolves `wide_canvas` per pane via the existing plugin registry
lookup by `Pane.Type` (no IPC change). Registry misses (unknown type) →
false → 1:1 behavior.

### 2. Preview renderer

`PaneModel` gains a "preview mode" predicate: canvas pane AND viewport
inner width < emulator width. In preview mode `renderContent` switches to a
wrap transform:

- Source rows: emulator screen rows (plus scrollback rows when scrolled).
- Each wide row is character-wrapped at the pane's inner width into 1..N
  visual rows (N = ceil(rowContentWidth / innerW); trailing blank cells of
  a row do not spawn extra visual rows — a blank 207-col row is 1 visual
  row).
- Cell walker preserves per-cell ANSI styling and skips `Width==0`
  wide-glyph continuation cells (same rules as `styledCellLine`).
- Live view is bottom-anchored: wrap the screen's rows, render the last
  `innerH` visual rows.
- Cursor: the software reverse-video caret (`insertCursor` equivalent) is
  mapped through the transform (wide (x,y) → visual (row,col)). The
  hardware-cursor path is unaffected (panes already use the software
  caret).
- Cache: wrapped visual rows are cached per pane keyed on
  `(contentGen, innerW)`; output bursts invalidate via the existing
  `contentGen` counter. The existing render cache keys gain the preview
  dimension so cached frames can't leak across modes.

### 3. Scrolling in preview mode

`ScrollUp/ScrollDown/ScrollToRelY` operate on visual rows when in preview
mode: the scroll offset counts visual rows over the wrapped concatenation
of (scrollback rows + screen rows). The scrollbar thumb math uses total
visual-row counts. Wrapping 10k scrollback rows per frame is avoided by the
same `(contentGen, innerW)` cache; counts are computed lazily.

Native-size and zoomed panes keep today's code paths untouched (the
transform is skipped when innerW == emulator width).

### 4. Selection

- Preview mode: mouse click focuses the pane only (no selection anchor);
  drag does nothing; shift-arrow selection keys are ignored. `lastContentLine`
  and the selection renderer are never consulted in preview mode.
- Zoomed/native: selection works exactly as today (grid coordinates match
  the emulator 1:1).

### 5. Plugin flag

- New plugin TOML field `wide_canvas = true` under `[display]` (sibling of
  `border_color` — it is a rendering concern), parsed into
  `DisplayConfig.WideCanvas bool`.
- claude-code and opencode defaults ship with `wide_canvas = true` and a
  `schema_version` bump; existing users get the standard migration dialog.
- Docs: `docs/plugin-reference.md` field table + `docs/features.md` note.

### 6. Revert of fix #3

`clearsOnWidthResize`, `pushScreenToScrollback`, their call in `ResizeVT`,
and `pane_resize_clear_test.go` are removed. `ResizeVT` returns to plain
resize-in-place. (Git revert of commit d1c3a14 plus doc-line cleanup.)

## Explicit limits

- Transcript text streamed before this feature (or at a different window
  width) keeps its original hard wrap; nothing can re-wrap it.
- Two attached TUI clients with different window sizes fight over canvas
  size (last resize wins) — unchanged from today's behavior for all panes.
- Preview shows claude's 207-col chrome as wrapped multi-row blocks by
  design (soft-wrap decision above).
- Preview selection deferred to v2.

## Testing

- `paneVTSize`: table-driven (canvas on/off, degenerate sizes).
- Wrap transform: wide glyphs at wrap boundary, styled cells, blank-row
  collapse, bottom anchoring, cursor mapping.
- Scroll math: visual-row offsets, thumb position, clamping at ends.
- Focus-mode invariant: toggling focus on a canvas pane calls ResizeVT with
  an unchanged size (no-op) — regression test for "zoom never resizes".
- resizeAllPanes sends canvas size for canvas panes, rect size otherwise.
- Plugin parsing: `wide_canvas` TOML round-trip; registry default false.
- Revert: pane_resize_clear tests removed with the code.
