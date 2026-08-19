# A single redraw kick still arms Claude Code's /clear hint

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/daemon/redraw.go`, `internal/plugin/defaults/claude-code.toml` |
| Found during | Fixing issue #169 (claude-code panes clearing themselves) |
| Date | 2026-08-19 |
| Tracked as | https://github.com/artyomsv/quil/issues/171 |

## Issue

Quil repaints a `claude-code` pane by writing `Ctrl+L` to its stdin. Since
v2.1.126 Claude Code treats the FIRST such press as "redraw, and show a hint
that pressing it again runs `/clear`", and the second within two seconds as
`/clear` itself.

`redrawKeyCooldown` (3 s) removes the case where Quil supplies both presses.
It does not remove the case where Quil supplies the first and the USER supplies
the second: someone who reattaches and then presses `Ctrl+L` themselves inside
two seconds — a reasonable reflex when a pane looks stale — clears the
conversation.

## Why it was deferred

Closing it means replacing the byte, and every candidate needs measurement
against a real PTY rather than a guess. The measured facts so far:

- `claude-code` emits 0 bytes on `SIGWINCH` in the CLASSIC renderer, so simply
  dropping `redraw_key` regresses classic-renderer users to the blank-pane bug
  that `redraw_key` was introduced to fix (PR #109).
- The pane enables focus reporting (`?1004h` appears in its output stream), so
  a focus-out/focus-in pair (`\x1b[O` `\x1b[I`) is a plausible repaint trigger
  that carries no keyboard semantics — unmeasured.
- Under the FULLSCREEN renderer the program is an alt-screen app, and Claude's
  own docs describe a window resize as the thing that repairs a stale frame,
  which suggests `SIGWINCH` may now be sufficient there. Also unmeasured, and
  the answer likely differs per renderer, which is what makes this moderate
  rather than trivial: the plugin schema has one `redraw_key` field and no way
  to say "this, unless the child is in fullscreen mode".

## What to do

Measure both candidates against a real `claude` process in each renderer
(`/tui default` and `/tui fullscreen`), the way the 2026-07-31 measurements in
`.claude/rules/daemon-lifecycle.md` were taken: spawn on a PTY, let it settle,
write the candidate, count the bytes that come back. If a semantics-free
trigger repaints in both renderers, adopt it and keep the cooldown as the
class-level guard for other plugins.
