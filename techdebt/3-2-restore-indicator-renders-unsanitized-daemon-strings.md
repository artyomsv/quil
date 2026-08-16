# The restore indicator renders daemon-supplied strings unsanitized and unbounded

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/tui/pane.go` — `restoreContext()`, `resumeLabel()`, reached from `renderRestoreIndicator` / `renderRestoreIndicatorCompact` |
| Found during | Security review of PR #166 (pending-pane and overlay boot indicators) |
| Date | 2026-08-16 |

## Issue

`restoreContext()` returns `p.Type + " · " + (p.Name or filepath.Base(p.CWD))` and
`restoreSteps()` renders `resumeLabel(p.Type, p.SessionID)` → `"starting " + paneType`
for any unrecognised type. All four interpolated fields — `Type`, `Name`, `CWD`,
`SessionID` — are copied verbatim from the daemon's `workspace_state` by
`syncPaneMeta` (`internal/tui/workstate.go`), and under `--remote` the daemon runs
on a host the user may not control.

Neither value passes `sanitizeRemoteText`, and neither is bounded. Both are drawn
through `lipgloss.Place`, which pads but never CLIPS — the property
`renderSpawnError`'s own comment in the same file documents, which is why
`SpawnError` is the one sanitized-and-elided value there.

`renderRestoreIndicator`'s `widest+2 > innerW` fallback is not a bound: it measures
with `ansi.StringWidth`, which counts an escape sequence as zero cells, so a
hostile string satisfies the check and still writes its escapes into the frame.
The indicator is not painted through the VT emulator that makes ordinary pane
output safe.

## Risks

- An OSC 52 in a pane `Type` or `Name` sets the user's system clipboard when the
  pane's boot indicator renders. A bidi override reverses the rendered line —
  U+202E is *printable*, so it passes any control-character filter.
- An over-wide block returns whole and the pane body grows past its rect, shifting
  the whole tab's `JoinHorizontal` — the row-drift family this codebase has
  shipped before.
- Reachable by anyone who can write the far host's `workspace.json` or speak to
  its socket, not only by full host compromise.

## Not introduced by PR #166, and not widened by it

That PR marks overlay panes `preparing`, which routes them into this render path —
but the capability is unchanged: `showRestoreIndicator()` fires on `Pending` alone,
and `syncPaneMeta` copies `Pending` for overlay panes too, so an overlay already
reached the indicator before that change. Tree panes have always reached it via
`rebuildTabs`. The exposure is identical either way.

Deliberately left out of #166: the fix is one line per field across four call
sites in a file that PR does not otherwise touch, and bundling it would bury a
security change inside a rendering fix.

## Suggested Solutions

1. Sanitize and bound at the two producers — `restoreContext()` and
   `resumeLabel()` — since every consumer is a render. Mirrors what
   `renderSpawnError` already does one function away (`elideMiddle(sanitizeRemoteText(...), innerW)`),
   so the idiom needs no invention. Preferred.
2. Sanitize in `syncPaneMeta` instead, i.e. at the wire boundary. Rejected on the
   precedent `remote-dialogs.md` sets: sanitize at RENDER only, because raw values
   are load-bearing elsewhere — `Pane.CWD` becomes a spawn directory and a
   comparison key for `paneInWorktree`, and `Type` selects a plugin.
3. Cap the fields at a `formMsgNameCap`-style constant as well as sanitizing.
   Needed regardless of 1 vs 2: sanitizing does not shorten, which is the
   distinction that produced this class of bug twice already in the sidebar.
