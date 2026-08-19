# claude-code ghost_buffer was measured against the classic renderer

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Moderate |
| Location | `internal/plugin/defaults/claude-code.toml` (`ghost_buffer`), `internal/daemon/daemon.go` (`handleAttach`) |
| Found during | Fixing issue #169 (claude-code panes clearing themselves) |
| Date | 2026-08-19 |

## Issue

`claude-code` ships `ghost_buffer = true` (2026-08-01, schema_version 11). The
measurement behind it was explicit: claude-code writes to the MAIN screen and
scrolls normally, so its output stream replays into coherent history.

That is a statement about the CLASSIC renderer. Claude Code's fullscreen
rendering draws on the alternate screen buffer like `vim`, and it is the default
for anyone whose first run was on or after 2026-05-06 — including the reporter
of #169, whose pane was confirmed on the alt screen (`?1049h` in its output).

A ring buffer of alt-screen output can begin mid-escape-sequence and encodes
absolute cursor positioning against a screen the replay does not own, which is
the exact condition `ghost_buffer = false` originally existed for. So the
default may now be wrong for most new users, in the direction of a corrupted
replay rather than a missing one.

## Why it was deferred

Issue #169 was a data-loss bug and had to ship on its own. This one is
cosmetic-to-confusing rather than destructive, and answering it properly means
measuring a replay in both renderers rather than reasoning about it.

Note the two are linked: with `ghost_buffer = true` an attach usually replays
and therefore does NOT call `redrawKick`, so the replay-versus-kick decision and
the redraw-key hazard are two views of the same attach path.

## What to do

Reattach to a fullscreen-renderer `claude-code` pane with a populated buffer,
both in-memory (TUI quit and relaunched) and from disk (daemon restarted), and
look at what the grid actually contains. If the replay is garbage there, the
options are per-renderer detection (the daemon can see `?1049h` in the pane's
own output stream) or reverting the default and accepting the blank-rectangle
cost that `redraw_key` covers.
