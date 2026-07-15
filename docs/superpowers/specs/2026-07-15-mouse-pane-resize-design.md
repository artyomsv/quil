# Mouse Pane Resize — Design

Date: 2026-07-15
Status: Approved

## Goal

Click-and-drag a border between two panes to resize them (and every pane in the
affected subtrees), with a minimum pane size floor. Ratios persist across
daemon restarts.

## Decisions (locked with user)

1. **PTY resize timing: on release only.** During the drag, only local state
   changes (`Ratio`, `tab.Resize()`, VT re-render). `MsgResizePane` per
   affected pane + `MsgUpdateLayout` for the tab fire once, on mouse release.
   Rationale: PTY resize churn garbled claude-code scrollback historically
   (2026-07-04 sidebar overlay fix); `wide_canvas` panes are immune either way.
2. **Disabled in notes mode.** Notes mode auto-enters focus mode (one visible
   pane, no inner borders) and squeezes the layout by `notesPanelWidth()`.
   Also disabled in: focus mode, overlay visible, single-pane tabs.
3. **Drag affordance: whole-border highlight.** Panes whose rect touches the
   dragged split line get a transient border color while the drag is active.

## Architecture

TUI-only change (approach A). No daemon, IPC schema, or persistence changes.
The daemon already stores layout opaquely (`MsgUpdateLayout`) and
`SerializedNode.Ratio` is already round-tripped, so persistence is free.

Rejected alternatives: daemon-authoritative split resize (inverts the
opaque-layout design for no benefit); absolute cell sizes à la tmux (rewrites
the layout tree + serialization for a cosmetic difference — ratios already
scale proportionally on window resize).

## Components

### 1. Border hit-testing — `internal/tui/layout.go`

```go
// BorderHit describes a draggable split line and the node that owns it.
type BorderHit struct {
    Node         *LayoutNode // internal node whose Ratio the drag mutates
    OX, OY, W, H int         // node's region at collection time (ratio math)
}
```

- `CollectBorders(ox, oy, w, h, out *[]BorderHit)` — walks the tree with the
  same arithmetic as `CollectRects`. For each internal node it emits one
  `BorderHit`. The split line is 2 cells thick — the two adjacent lipgloss
  border cells (H-split: columns `ox+leftW-1` and `ox+leftW` across the node's
  full height; V-split: rows `oy+topH-1` and `oy+topH` across the full width).
- Hit-test scans the collected slice **in reverse** so the deepest matching
  node wins at T-junctions (emission order = depth order).
- Subtree minimums (pure functions):

```go
func (n *LayoutNode) minWidth() int  // leaf: minPaneW; H-split: Left+Right; V-split: max(Left, Right)
func (n *LayoutNode) minHeight() int // symmetric
```

### 2. Drag state machine — `internal/tui/model.go`

Follows the scrollbar-drag pattern exactly.

- New fields: `splitDragNode *LayoutNode`, `splitDragRect BorderHit`
  (geometry captured once at click — immune to mid-drag layout shifts).
  Both are added to `clearDragState()`.
- **MouseClickMsg** (left button, pane area): `hitTestSplitBorder(x, y)` runs
  after the notes-editor and scrollbar checks and before the pane-selection
  arm. On hit: `clearDragState()`, arm the drag, set highlight flags on
  adjacent leaves.
- **MouseMotionMsg**: clamp in cells, then derive the ratio (exact at
  boundaries, no float truncation flicker):

```go
leftW := clamp(msg.X-node.OX, node.Left.minWidth(), node.W-node.Right.minWidth())
node.Ratio = float64(leftW) / float64(node.W)
```

  (V-split symmetric with Y/H.) Then `tab.Resize(...)` — local only, no IPC.
  Guard: re-validate `splitDragNode` is still reachable in the active tab's
  tree each motion (workspace reconciliation can promote/remove nodes);
  silently drop the drag if gone.
- **MouseReleaseMsg**: send `MsgResizePane` for the dragged node's leaves
  (sizes via `paneVTSize`, daemon drops same-size duplicates) +
  `MsgUpdateLayout` for the tab, clear highlight flags, `clearDragState()`.

Precedence/guards already handled upstream of this branch: sidebar overlay
swallow, lazygit overlay swallow, tab bar (Y=0), status bar (Y=height-1).
The scrollbar check stays first — its 3-cell hit zone keeps priority where it
overlaps a pane's right border; border drag owns the neighbouring column and
any Y outside the scrollbar's content range.

### 3. Drag highlight — `internal/tui/pane.go`

- New transient `PaneModel.splitDragHighlight` bool (runtime-only, never
  persisted) set on leaves whose rect touches the dragged split line, on both
  sides; cleared on release/drop.
- Rendering: one new color slot in the `View()` border-color precedence chain,
  above `Active`, below `mcpHighlight`. Whole-border recolor — same mechanism
  as every existing state color; `buildTopBorder` already takes a color, no
  signature change.
- The flag joins `renderKey()` so the view cache invalidates on toggle.

## Data flow

1. Click on split line → `BorderHit` captured, highlight on.
2. Motion → `Ratio` mutated, `tab.Resize()` recomputes every affected pane's
   rect + local VT (`resizeNode`). Render follows. No IPC.
3. Release → `MsgResizePane` (per leaf) + `MsgUpdateLayout` (per tab) →
   daemon resizes PTYs (children reflow once) and persists the layout blob →
   next `workspace_state` broadcast is a no-op reconciliation.

## Error handling

- Pane/node destroyed mid-drag → drag dropped silently (same as scrollbar
  drag's `activePaneByID` guard).
- Ratio clamped so every leaf in both subtrees keeps `minPaneW`/`minPaneH`
  (10×4, existing constants) — nested splits protected via subtree minimums.
- Degenerate node (`W`/`H` of 0) → hit-test emits nothing; motion divides only
  by captured `node.W > 0`.

## Testing

- `layout_test.go` (table-driven): `CollectBorders` for H, V, and nested mixed
  splits; `minWidth`/`minHeight` on nested trees (sum vs max); clamp math at
  both extremes (ratio pinned so both subtrees sit exactly at minimum).
- `model_test.go`: extend `TestModel_ClearDragState` with the new fields;
  drag lifecycle (click → motion mutates ratio → release emits `MsgResizePane`
  + `MsgUpdateLayout` via `fakeSender`); guards (focus mode, notes mode,
  overlay, single pane) never arm the drag.
- Manual: dev daemon (`verify` skill), drag next to a claude-code pane,
  confirm no mid-drag PTY churn and post-release reflow is clean.

## Scope

~4 files, all `internal/tui/`: `layout.go`, `model.go`, `pane.go`, plus tests.
No daemon, IPC, or persistence changes.
