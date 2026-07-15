# Terminal pane width shrink loses line content (no reflow-on-resize)

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Epic |
| Location | `internal/tui/pane.go:360` (`ResizeVT`), upstream `charmbracelet/x/vt` `Emulator.Resize` |
| Found during | Mouse split-border drag-resize testing (feature branch `feat/mouse-pane-resize`) |
| Date | 2026-07-15 |

## Issue

When a **terminal** pane's width shrinks (split-border drag release, window
resize, sidebar-less layout changes), on-screen line content that no longer
fits is cut, and growing the pane back does not restore it. Two layers
conspire, neither of which reflows:

1. **Local emulator**: `x/vt`'s `Resize` preserves the grid by cropping —
   cells beyond the new width are dropped, and scrollback rows are not
   rewrapped. There is no reflow-on-resize (tmux, Windows Terminal, and
   iTerm all rewrap from their own history here).
2. **ConPTY side**: after the PTY resize, ConPTY re-renders only the
   viewport. Whether conhost itself truncated or wrapped (inbox conhost
   truncates; newer OpenConsole reflows), rows that scrolled out of the
   viewport during the narrow phase are never re-emitted, so our emulator
   cannot recover them even when conhost still has them.

AI panes (`wide_canvas`) are immune by design — their emulator/PTY stay
window-sized and the app repaints itself.

This is NOT specific to the drag-resize feature: shrinking the whole Quil
window and growing it back does the same through the identical
`ResizeVT` + `MsgResizePane` path. The drag feature just makes extreme
shrinks easy to perform, which surfaced it.

## Risks

Users lose visible terminal output (not the shell's own history — commands
can be re-run) when squeezing panes. Perceived as data corruption; erodes
trust in pane resizing even though the underlying bytes were emitted at the
old width and are simply unrecoverable at the grid layer.

## Suggested Solutions

1. **Reflow-on-resize in the emulator layer** (proper fix, matches tmux):
   on width change, rewrap screen+scrollback rows using soft-wrap
   continuation flags. `x/vt` tracks wrapped lines internally; needs an
   upstream feature (or a fork/wrapper that reconstructs logical lines from
   the grid + scrollback and re-lays them out at the new width). Epic:
   correctness across CJK/wide cells, cursor mapping, alt-screen exclusion.
2. **Replay-based reflow for plain terminal panes only**: rebuild the VT
   from `rawBuf` at the new size on resize *when the pane never entered the
   alt screen* (shells only — `ResizeVT`'s comment explains why replay is
   wrong for TUI apps). Partial: ConPTY streams carry per-row cursor
   positioning, so historical rows may not unwrap; needs experimentation.
3. **Do nothing, document**: parity with plain conhost behavior; content
   loss is bounded to what was on screen during the narrow phase.
