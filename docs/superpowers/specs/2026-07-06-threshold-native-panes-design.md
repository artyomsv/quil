# Threshold-native AI panes + preview selection — Design

Date: 2026-07-06
Status: Approved (brainstorming) — pending implementation plan
Area: `internal/tui` (wide-canvas), `internal/plugin` (config)

## Problem

The wide-canvas feature (PR #84) keeps AI panes (`[display] wide_canvas`,
claude-code + opencode) on a **window-sized** PTY/emulator regardless of their
layout rect, then renders a **preview** into small rects — left-edge crop by
default, soft-wrap via `Alt+Shift+W`. This exists to avoid PTY-resize churn,
which garbles Claude's hard-wrapped transcript.

Two consequences the user hit:

1. **Mouse selection stopped working in splits.** By design, selection is
   disabled whenever a pane is in `previewMode()` (a wide-canvas pane narrower
   than its emulator — i.e. any split). It works only in a single full-width
   pane or in zoom (`Ctrl+E`). This is a deliberate v1 limitation
   (`model.go` comments: "zoom-only for selection (v1)"), not a regression.

2. **50%-split panes are cropped.** At ~50% width the preview left-crops off
   the right half of Claude's output. In a 2-pane split the user does not zoom
   and wants to read the pane directly, so the crop wastes the readable width.

## Goals

- A wide-canvas pane that is **wide enough to be readable** renders natively
  (real pane-width PTY, no crop, Claude's own layout).
- Panes too narrow to be readable keep the wide canvas + preview.
- **Mouse** selection works in every case: native panes (free), zoom (free),
  and narrow preview panes (new inverse-map).

## Non-goals

- Reflowing already-wrapped transcript (impossible; upstream Claude behavior).
- Keyboard (`shift+arrow`) selection inside narrow preview panes — stays
  disabled there (works natively and in zoom). Stepping a caret by logical
  lines through a wrapped view is disorienting.
- Any change to the OpenCode history-extraction follow-up or other unrelated
  wide-canvas work.

## Design overview

The whole feature turns on one decision: **is this wide-canvas pane wide enough
to render natively?** Threshold = `min_native_cols` (default 80 — Claude's
comfortable minimum). The threshold logic lives in exactly one function,
`paneVTSize`. Everything else follows.

Key property: `previewMode()` is defined as `WideCanvas && innerW < vt.Width()`.
Because the emulator width is set *by* `paneVTSize`, once `paneVTSize` returns
the **rect** size for a wide-enough pane, `vt.Width() == innerW`, so
`previewMode()` becomes `false` automatically. Native panes therefore re-enable
mouse **and** keyboard selection with zero changes to selection code. The
`locate()` inverse-map (Section 3) is only needed for genuinely-narrow panes.

## Section 1 — Threshold-driven canvas sizing (`internal/tui/canvas.go`)

`paneVTSize` gains a `minNativeCols` parameter and one new rule: use the window
canvas **only when the rect is too narrow**.

```go
const defaultMinNativeCols = 80

func paneVTSize(wideCanvas bool, minNativeCols, rectW, rectH, canvasW, canvasH int) (cols, rows int) {
    w, h := rectW, rectH
    if minNativeCols <= 0 {
        minNativeCols = defaultMinNativeCols
    }
    if wideCanvas && canvasW > 0 && canvasH > 0 && rectW-2 < minNativeCols {
        w, h = canvasW, canvasH // too narrow → wide canvas + preview
    }
    // else: rect ≥ threshold → native pane-width PTY (rect sizing)
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

- **inner width ≥ `min_native_cols`** → PTY/emulator = rect → Claude renders
  natively, no crop. The pane resizes like a normal pane on layout changes
  (split drag, notes panel, zoom, window resize). Accepted trade-off — the
  resize churn the wide canvas avoided is the price for a native look, paid
  only for panes wide enough to want it.
- **inner width < `min_native_cols`** → unchanged wide-canvas + preview.
- Comparison uses **inner** width (`rectW-2`, border-adjusted), matching the
  `cols = w-2` grid convention.
- Zero canvas (tests, before first `resizeTabs`) still falls back to rect.

Thread the new argument through the three callers, each passing
`pane.MinNativeCols`:
- `internal/tui/layout.go:423` — `resizeTabs` tree walk.
- `internal/tui/model.go:4069` — PTY resize message to the daemon (keeps the
  real PTY in lockstep, so the daemon-side child resizes too).
- `internal/tui/tab.go:313` — focus-mode viewport resize.

Note: because `model.go:4069` feeds the daemon `MsgResizePane`, crossing the
threshold sends a real resize to the child. The daemon's same-size resize guard
(`Pane.appliedCols/appliedRows`) already collapses redundant resizes, so
idle re-broadcasts do not churn.

## Section 2 — Config plumbing (`min_native_cols`)

- `internal/plugin/plugin.go` — `DisplayConfig` gains `MinNativeCols int`.
- `internal/plugin/registry.go` — TOML tag `min_native_cols` on the parse
  struct; copied into `DisplayConfig` alongside `WideCanvas`.
- `internal/tui/pane.go` — `PaneModel.MinNativeCols int` (new field).
- `internal/tui/model.go` — `pluginMinNativeCols(paneType string) int` resolver,
  mirroring `pluginWideCanvas`.
- `internal/tui/workstate.go` — `syncPaneMeta` gains a `minNativeCols` parameter
  and sets `pane.MinNativeCols`; every call site (`model.go` reconciliation
  paths + overlay + restore) passes `m.pluginMinNativeCols(info.Type)`.

**Default handling:** the code default (0 → 80) lives in `paneVTSize`. Therefore
the shipped `claude-code.toml` / `opencode.toml` are **not** modified and
`schema_version` is **not** bumped — no forced migration dialog for existing
users. `min_native_cols` is an opt-in override, documented in
`docs/plugin-reference.md` under `[display]`.

## Section 3 — Mouse selection in narrow preview panes

### 3a. Inverse-map helper (`internal/tui/pane_preview.go`)

```go
// previewPosAt maps a pane-local (relX, relY) — already border-adjusted — to an
// emulator (col, absLine) via the visual→absolute layout. ok=false when the
// point is outside the rendered content.
func (p *PaneModel) previewPosAt(relX, relY int) (col, absLine int, ok bool)
```

- `innerW = Width-2`, `innerH = Height-2`, `l = previewLayoutFor(innerW)`.
- `total = l.totalVisual()`; `viewStart = total - innerH - scrollBack`, clamped
  to `>= 0` (mirrors `renderPreview`).
- `v = viewStart + relY`; if `v < 0 || v >= total` → `ok = false`.
- `absRow, s = l.locate(v)`.
- `col = s.start + relX`, clamped to `[s.start, min(s.end, contentEnd+1))` so a
  click in the blank area past content maps to line end, not off-grid.
- `absLine = absRow` (already absolute across scrollback+screen, matching how
  `Selection.Line = sbLen + screenY`).
- Works identically for crop mode (one segment per row) and wrap mode (N
  segments) because `locate` handles both.

### 3b. Mouse handlers (`internal/tui/model.go`)

- Remove the `armSelection = false` preview guard (~`model.go:619`) so a click
  on a preview pane arms drag-selection.
- In the coord-mapping block (~`model.go:3708`–`3762`), when the resolved pane
  is in `previewMode()`, compute `startCol/startLine` from
  `previewPosAt(mouseStartX-ox-1, mouseStartY-oy-1)` and `curCol/curLine` from
  `previewPosAt(curX-ox-1, curY-oy-1)` instead of the direct
  `x-ox-1` / `sbLen-scrollBack+(y-oy-1)` mapping. Non-preview panes keep the
  existing mapping. If `previewPosAt` returns `ok=false`, clamp to the nearest
  valid visual row (top/bottom of the rendered window) so a drag past the pane
  edge still selects to the boundary.

### 3c. Selection rendering (`internal/tui/pane_preview.go`, `renderPreview`)

`renderPreview` currently draws only the reverse-video caret. Extend it to draw
the selection highlight:

- For the active pane with a live selection whose `PaneID` matches, for each
  visual row `v → (absRow, s)`: compute the row's selection column range via
  `Selection.ColRange(absRow, innerW)`, intersect `[selStart, selEnd)` with the
  segment window `[s.start, s.end)`, translate to local columns
  (`selStart-s.start`, `selEnd-s.start`), and pass to
  `styledCellLineWithSelection`. This mirrors the notes soft-wrap selection
  intersection already in `TextEditor`.
- The caret path is unchanged and coexists (caret is a 1-cell selection when no
  range selection covers it).

### 3d. Scope boundary

Keyboard selection stays disabled in narrow preview: `handleSelectionKey`'s
`previewMode()` early-return (~`model.go:3792`) is unchanged. Native panes and
zoom keep full keyboard selection.

## Section 4 — Testing

New / extended tests (white-box `package tui` where internals are touched):

- **`canvas_test.go`** — `paneVTSize` table: wide + narrow rect → canvas; wide +
  rect ≥ threshold → rect; `minNativeCols <= 0` → default 80; non-wide always
  rect; zero canvas → rect fallback.
- **`pane_preview_test.go`** — `previewPosAt` round-trips: crop mode single
  segment; wrap mode multi-segment (relY lands on the right segment); scrolled
  view offset; wide-glyph boundary; out-of-range `relY` → `ok=false`; click past
  content-end clamps to line end.
- **`pane_preview_test.go`** — preview selection render: a selection spanning a
  wrap boundary highlights contiguously across visual rows; a single-segment
  crop row highlights the intersected span only.
- **propagation** — `syncPaneMeta` sets `MinNativeCols` on the normal and
  restore reconciliation paths; `pluginMinNativeCols` returns the TOML value and
  the 0-default.
- Full suite + `-race` + `vet` green via `./scripts/dev.sh test` /
  `test-race` / `vet` (Docker; host has no Go).

## Files touched

| File | Change |
|------|--------|
| `internal/tui/canvas.go` | `paneVTSize` gains `minNativeCols`; threshold rule; `defaultMinNativeCols` |
| `internal/tui/layout.go` | pass `pane.MinNativeCols` to `paneVTSize` |
| `internal/tui/tab.go` | pass `pane.MinNativeCols` to `paneVTSize` |
| `internal/tui/model.go` | pass through at PTY-resize site; `pluginMinNativeCols`; drop preview select guard; preview coord branch |
| `internal/tui/pane.go` | `PaneModel.MinNativeCols` field |
| `internal/tui/workstate.go` | `syncPaneMeta` sets `MinNativeCols` |
| `internal/tui/pane_preview.go` | `previewPosAt`; selection rendering in `renderPreview` |
| `internal/plugin/plugin.go` | `DisplayConfig.MinNativeCols` |
| `internal/plugin/registry.go` | parse `min_native_cols` |
| `docs/plugin-reference.md` | document `[display] min_native_cols` |
| `.claude/CLAUDE.md` | update wide-canvas note (threshold + preview selection) |
| tests | `canvas_test.go`, `pane_preview_test.go`, propagation tests |

## Risks & mitigations

- **Resize churn returns for wide panes.** Mitigated: only above the threshold,
  where the pane is wide enough that occasional re-wrap is acceptable; the
  daemon same-size guard collapses redundant resizes; sidebar is already an
  overlay (no resize).
- **Threshold flip-flop while dragging a split across 80 cols** re-wraps Claude
  each crossing. Acceptable — the user is actively dragging; real terminals
  re-wrap during drag too. No hysteresis in v1 (add later if it annoys).
- **`previewPosAt` off-by-one at segment/wide-glyph boundaries** — covered by
  round-trip tests against `visualIndex`/`locate`.
