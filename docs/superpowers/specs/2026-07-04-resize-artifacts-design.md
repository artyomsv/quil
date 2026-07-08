# Pane Resize Artifacts — Design

**Date:** 2026-07-04
**Status:** Approved

## Problem

Claude-code panes accumulate garbled content — text wrapped at a stale width mixed
with full-width text, plus duplicated transcript chunks — after working in the
background and then being focused/zoomed. Root cause chain:

1. Claude Code hard-wraps its transcript at the width it was written; no terminal
   can re-wrap it later (explicit newlines).
2. Claude Code re-renders its viewport on every PTY resize. Quil's VT emulator
   (`charmbracelet/x/vt`) does not reflow on resize, so the old narrow frame stays
   on screen and the new wide frame prints below it — mixture + duplication.
3. Quil resizes panes far more often than a normal terminal window gets resized:
   - Alt+N notification sidebar toggle squeezes **every pane on every tab**
     (`paneAreaWidth`, `resizeTabs` loop).
   - Focus mode (Ctrl+E) resizes the active pane grid↔full (~57↔210 cols).
   - `paneAreaWidth` depends on the *active* tab's focus state, so with the
     sidebar open, tab switches oscillate every pane's width.
   - `resizeAllPanes` fires on every workspace-state broadcast and the daemon's
     `handleResizePane` applies `PTY.Resize` with no same-size check (currently
     benign — ConPTY no-ops equal dims — but zero-cost churn).

Mechanisms 1–2 are upstream/inherent; 3 is Quil's amplifier and the fix target.

## Fix 1 — Notification sidebar becomes an overlay

The sidebar stops occupying layout space. It draws **on top of** the tab content's
rightmost `notifications.width` columns. Panes keep full width; zero PTY resizes
on Alt+N.

- `Model.paneAreaWidth()` returns `m.width` unconditionally (sidebar branch
  deleted). The focus-mode/sidebar width interplay disappears.
- View composition: replace the sidebar `lipgloss.JoinHorizontal` with a
  compositor `overlayRight(base, overlay string, totalW, overlayW int) string` —
  per line: ANSI-aware truncate of the base line to `totalW-overlayW` columns
  (`x/ansi.Truncate`, wide-glyph safe), SGR reset, append the overlay line.
  Applied to the tab content block only; tab bar and status bar untouched.
- Sidebar is drawn in focus mode too (previous `tab.FocusMode()` suppression
  removed) and over the notes editor when both are visible — one rule:
  visible ⇒ drawn over whatever is beneath.
- `notesSidebarWidth` reservation is deleted; notes panel math uses `m.width`.
- Too-narrow guard kept: sidebar not drawn when `m.width - sidebarW < minTermWidth`.
- Mouse: while the sidebar is visible, click/wheel events with
  `x >= m.width - sidebarW` (below the tab bar) are swallowed so they don't hit
  the pane underneath. Sidebar remains keyboard-driven.
- Alt+N / Ctrl+Alt+N handlers drop `resizeAllPanes()`; keep `tea.ClearScreen`.

## Fix 2 — Same-size resize guard (daemon-side only)

`handleResizePane` skips `PTY.Resize` when the requested cols/rows equal the last
**applied** size. Tracking fields live on the daemon `Pane` and are zeroed
whenever a PTY is (re)created — spawn, respawn on restore, restart — so a fresh
PTY always accepts its first resize (no stuck-at-default-size regression).

No TUI-side skip: the TUI keeps sending on every broadcast (negligible bytes) and
the daemon filters. One guard, one owner, no stale-cache risk across restarts.
`resizeKick`'s 1-col jiggle calls `PTY.Resize` directly, not through the handler —
unaffected.

## Fix 3 — Clean screen on width change (AI panes, mainscreen)

In `PaneModel.ResizeVT`, when **width** changes AND `!isTerminalPane(p)` AND
`!p.vt.IsAltScreen()`:

1. Find the last non-blank screen row `L` (reuse `lastContentLine`).
2. Feed synthetic bytes to the emulator only (the child never sees them):
   `CUP(bottom-left)` + `L+1` line feeds — scrolls exactly the content rows into
   scrollback without trailing-blank spam — then `ESC[2J` + `ESC[H`.
3. Resize the emulator as today.

After a zoom the visible pane shows only the child's fresh repaint at the new
width; the old narrow-wrapped frame lives in scrollback above it. Claude's
post-resize repaint opens with relative positioning (CR/CUU/ED) which clamps
harmlessly on a homed blank screen.

Out of scope: height-only changes, terminal/ssh panes (no repaint-on-resize; a
clear would only hide context), altscreen apps (k9s, lazygit — excluded by the
`IsAltScreen` gate). No config knob in v1.

## Testing

- `overlayRight`: plain/ANSI-styled lines, wide glyph (CJK/emoji) at the cut
  boundary, base narrower than cut width, overlay taller/shorter than base.
- `paneAreaWidth` full-width with sidebar visible; notes width math without
  sidebar reservation; existing `notes_test.go` reservation tests updated.
- Click-swallow: click inside sidebar region reaches no pane; outside unaffected.
- Daemon guard: duplicate resize skipped; first resize after PTY recreation
  passes through; different size passes through.
- Fix 3 emulator tests: narrow wrapped content → widen ⇒ screen clean, old
  content in scrollback; altscreen pane untouched; terminal pane untouched;
  height-only change untouched.

## Explicitly not fixable in Quil

The narrow-wrapped historical transcript itself and Claude Code's
duplicate-on-resize re-render (also visible in tmux zoom). Scrollback reflow in
the emulator would be the principled long-term fix but `x/vt` doesn't support it
and hard-wrapped lines cannot be reflowed anyway.
