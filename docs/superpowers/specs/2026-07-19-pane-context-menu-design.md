# Pane Context Menu (Right-Click) — Design

Date: 2026-07-19
Status: Approved

## Goal

Right-click on a pane opens a small flyover menu near the mouse cursor with
per-pane actions (input history, focus mode, notes, lazygit, rename, mute,
attention mark, restart, close). The menu targets the pane **under the
cursor** — not necessarily the active pane — and highlights that pane's border
while open so the target is unambiguous. All actions dispatch into existing
keybinding handler logic; the menu is a second dispatcher, not a second
implementation.

## Decisions (locked with user)

1. **Right-click split by selection.** A right-click while a text selection is
   active keeps today's behavior (copy to clipboard). With no selection,
   right-click opens the context menu. Zero muscle-memory breakage.
2. **Attention mark is pinned, session-only.** New `PaneModel.pinnedAttention`
   bool, toggled only via the menu (Mark/Unmark attention). Renders the same
   green border as the `unseen` work-state mark, but `ackFocusedPane` does NOT
   clear it — focusing the pane is not an acknowledgement of a deliberate pin.
   Not persisted; lost on TUI restart. No IPC/daemon changes.
3. **v1 item list (9 items):** input history, focus mode, notes, lazygit,
   rename pane, mute/unmute, mark/unmark attention, restart pane…, close
   pane…. Restart/close keep their existing confirm dialogs.
4. **Keyboard entry point.** The dormant `[keybindings] quick_actions`
   (reserved since M1, never implemented) opens the same menu for the
   **active** pane, anchored at that pane's top-left content corner. Gives
   keyboard parity and makes the menu testable without synthesizing mouse
   events. **Default rebound from `ctrl+a` to `alt+a`**: activating the old
   `ctrl+a` default would steal readline beginning-of-line from every shell
   pane (and the common tmux prefix). `alt+a` follows the existing Alt-layer
   convention (Alt+N/M/E/G…) and collides with nothing bound today; the
   config field name `quick_actions` is unchanged, so users who already
   customized it keep their value.

## Architecture

TUI-only change. No daemon, IPC schema, or persistence changes. The menu is a
**compositor overlay** (like the notification sidebar), NOT a `dialogScreen`:
dialogs are modal and centered; this popup is positional, non-modal in
spirit (dismiss on outside click), and must coexist with the sidebar in the
same compositing pass. Reserving layout width is forbidden for the same
reason as the sidebar — it would resize PTYs and re-trigger the claude-code
repaint garbling (see 2026-07-04 resize-artifacts design).

Rejected alternatives:
- **New `dialogScreen` kind** — dialogs center themselves, block all pane
  interaction, and have no concept of anchor coordinates; wrong shape.
- **Daemon-side menu state** — the menu is pure view state; nothing about it
  outlives a frame except the pinned-attention flag, which is deliberately
  session-local.
- **Always-menu on right-click with a Copy item** — breaks the established
  right-click-copies flow for a rare gain; rejected with user.

## Components

### 1. Positioned overlay compositor — `internal/tui/compose.go`

```go
// overlayAt composites box onto base with box's top-left cell at (x, y).
// base is a block of totalW-wide lines. ANSI-aware like overlayRight:
// the base line is split with ansi.Truncate and closed with an SGR reset
// on both sides of the inserted box segment so styling never bleeds.
func overlayAt(base, box string, x, y, totalW int) string
```

- Sibling of `overlayRight`, same ANSI discipline (wide glyphs straddling a
  cut are dropped and space-padded; `\x1b[0m` before padding).
- Pure function; position clamping is the **caller's** job (`ctxMenuPos`).
- Composition order in `View()`: tab content → sidebar (`overlayRight`) →
  context menu (`overlayAt`). Menu draws last so it wins over the sidebar if
  they overlap.

### 2. Menu state + widget — `internal/tui/ctxmenu.go` (new file)

```go
type ctxMenuItem struct {
    id      ctxMenuAction // typed enum: ctxActHistory, ctxActFocus, …
    label   string        // resolved per open ("Mute notifications" vs "Unmute…")
    enabled bool          // greyed + cursor-skipped + click-inert when false
}

type ctxMenuState struct {
    paneID string        // target pane; "" = menu closed
    x, y   int           // clamped top-left of the rendered box
    cursor int           // index into items; always on an enabled item
    items  []ctxMenuItem
}
```

- `Model.ctxMenu ctxMenuState` — closed state is the zero value.
- `buildCtxMenuItems(m, pane)` resolves the 9 items per open:
  - **Input history** — enabled iff `m.pluginRegistry.Get(pane.Type).Command.RecordHistory`
    (same probe as the `kb.CommandHistory` case, model.go).
  - **Lazygit** — enabled iff the lazygit binary resolves (reuse the
    availability gate in `internal/tui/overlay.go`); greyed styling reuses the
    uninstalled-plugin look from the Ctrl+N dialog.
  - **Mute/Unmute**, **Mark/Unmark attention** — label reflects current state.
  - Everything else always enabled.
- `renderCtxMenu` draws a lipgloss-bordered vertical list, sized to the
  longest label, cursor row inverted, disabled rows in the greyed style.
- `ctxMenuPos(anchorX, anchorY, boxW, boxH, screenW, screenH)` — pure clamp:
  prefer the box's top-left at the cursor cell (+1,+1 so the pointer doesn't
  cover the first item); shift left/up as needed so the whole box stays on
  screen (never clipped, never over the tab bar row 0 or status bar row H-1).
- `ctxMenuHitRow(x, y)` — inverse of the render layout for click/hover
  mapping.

### 3. Open/close + target highlight — `internal/tui/model.go`

Open (mouse path), inserted in the `tea.MouseClickMsg` right-click branch
AFTER the selection-copy checks (decision 1):

1. Existing suppressions run first (Ctrl held, lazygit overlay visible,
   sidebar region swallow). Additional suppressions: any dialog open
   (`m.dialog != dialogNone`), notes mode, tab rename editing, focus-mode
   handled naturally (hit-test sees the single full-rect pane).
2. Hit-test the pane under the cursor: collect rects exactly as the
   scrollbar/border hit-tests do (`tab.Root.CollectRects(0, 1, …)`) and find
   the rect containing (X, Y). Miss (border rows/status bar) → no-op.
3. `m.clearDragState()` (one-interaction-at-a-time invariant), clear
   `m.selection`, populate `m.ctxMenu`, set the target-pane highlight.
4. Right-click while the menu is already open **re-targets**: close + reopen
   at the new cursor/pane in one step (OS-menu convention).

Open (keyboard path): `kb.QuickActions` case in the main key switch → same
state populate for the active pane, anchored at its content top-left
(rect OX+1, OY+1).

Target highlight: new `PaneModel.ctxTargetHighlight` bool, rendered with the
same transient-highlight color as `splitDragHighlight` (color 39) and included
in the pane's `renderKey` so the border repaints on set/clear. Set on open,
cleared on close (all close paths).

Close paths: Esc, item executed, outside click, `clearDragState`-triggering
interactions, workspace reconciliation removing the target pane
(`applyWorkspaceState` prunes → close menu, mirror of the mid-drag prune in
split-drag).

### 4. Input routing while open — `internal/tui/model.go`

Keyboard (checked before the main key switch, after the dialog branch):
- `up`/`down` — move cursor, skipping disabled items (wrap around).
- `enter` — execute cursor item.
- `esc` — close, no action.
- `kb.Quit` — close menu, then fall through to the normal quit path (quit
  must never be swallowed).
- Everything else — swallowed (menu is keyboard-capturing while open; it is
  short-lived, so no other exempts needed).

Mouse (checked early in each mouse case, after the overlay/sidebar swallows):
- Motion inside the box — hover moves cursor (disabled rows ignored).
- Left-click inside the box — execute that row (disabled rows inert).
- Left-click outside — close, then let the click fall through to nothing
  (swallow it; a close-click should not also arm a selection drag).
- Right-click outside on another pane — re-target (see above).
- Wheel — swallowed while open.

### 5. Dispatch — `internal/tui/ctxmenu.go`

```go
func (m Model) executeCtxMenuItem(item ctxMenuItem) (Model, tea.Cmd)
```

Sequence: capture target paneID → close menu (clear state + highlight) → set
`tab.ActivePane = paneID` (pane focus is TUI-local today; the
`setActivePaneMsg` MCP handler does exactly this) → dispatch by `item.id` to
the **existing** handler logic:

| Item | Existing path |
|---|---|
| Input history | `RecordHistory` probe + `openHistoryDialog` + `requestHistory` (the `kb.CommandHistory` case body, extracted to a named method) |
| Focus mode | `kb.FocusPane` case body |
| Notes | `kb.NotesToggle` case body |
| Lazygit | `m.handleToggleLazygit()` |
| Rename pane | `kb.RenamePane` case body |
| Mute/Unmute | `m.toggleActivePaneMute()` |
| Mark/Unmark attention | new `pane.pinnedAttention = !pane.pinnedAttention` |
| Restart pane… | `kb.RestartPane` case body (opens `confirmKindRestartPane`) |
| Close pane… | `kb.ClosePane` case body (opens close confirm) |

Inline case bodies that the menu reuses are extracted into named `Model`
methods in place (no behavior change); cases that are already one-line helper
calls are called directly. No action logic is duplicated.

### 6. Pinned attention — `internal/tui/pane.go`, `tab.go`, `model.go`

- `PaneModel.pinnedAttention bool` — runtime-only, never serialized.
- Border render: green border iff `unseen || pinnedAttention` (same style,
  same precedence slot below active/ghost/MCP-highlight).
- `ackFocusedPane` continues to clear only `unseen`; it never touches
  `pinnedAttention`.
- Tab label green derivation: `tabUnseen` ORs in pinned panes so a background
  tab with a pinned pane shows the green label. The active tab keeps today's
  exception for `unseen` but DOES show green when a pinned pane exists in it
  and is not the focused pane — a pin is an explicit "don't let me forget",
  not a seen/unseen state. (The focused pane itself never colors the label —
  you are looking at it.)

## Error handling

- Target pane destroyed while menu open (daemon reconciliation) → close menu
  + clear highlight; no dangling paneID deref (`activePaneByID` nil-check
  pattern).
- Hit-test miss / zero panes → menu never opens.
- Disabled item execution is unreachable (cursor skips, clicks inert) —
  `executeCtxMenuItem` still no-ops on `!item.enabled` as a backstop.
- Window resize while open → close menu (cheapest correct answer; anchor
  coordinates are stale after reflow).

## Testing

- `overlayAt` — table-driven: ANSI truncation at both cut points, wide-glyph
  straddle, box taller/wider than base, reset-before-pad bleed guard.
- `ctxMenuPos` — pure clamp math: all four edges, tab-bar row and status-bar
  row exclusion.
- `buildCtxMenuItems` — enabled/disabled matrix (RecordHistory on/off, lazygit
  binary present/absent via the existing swappable gate), state-dependent
  labels (muted, pinned).
- Menu lifecycle via `Update` with synthesized messages: `ctrl+a` opens for
  active pane (keyboard-only path, no mouse synthesis needed); right-click
  with selection copies (regression); right-click without selection opens;
  Esc/outside-click/re-target; cursor skip over disabled; dispatch reaches
  the confirm dialog for close/restart.
- `pinnedAttention`: survives `ackFocusedPane`; toggles via menu; tab label
  derivation.
- `clearDragState` invariant test extended (menu-open clears drags; drags
  close menu).

## Out of scope (v1)

- Persisting the attention mark (daemon field + workspace.json) — revisit if
  session-only proves insufficient.
- Right-click on the tab bar (tab context menu).
- Split H/V menu items.
- Per-plugin custom menu items (TOML `[[menu]]` section).
- Forwarding right-click to mouse-tracking apps in the pane.
- Hover-open submenus, icons, separators.
