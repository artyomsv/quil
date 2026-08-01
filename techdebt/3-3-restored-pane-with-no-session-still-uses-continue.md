# A restored claude-code pane with no recorded session still falls back to `--continue`

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/daemon/daemon.go` — `claudeResumeTemplate`, the `!sawRecorded` branch |
| Found during | Code review of the claude-resume-session-hijack fix (PR #119) |
| Date | 2026-08-01 |

## Issue

PR #119 removed the `--continue` fallback for every restored claude-code pane
that has a recorded session id, because `--continue` is Claude's
most-recent-session-in-CWD lookup and therefore attaches the pane to whichever
sibling pane's conversation was touched last. One case was deliberately left
alone: a pane that recorded **no** session at all still returns the plugin's
configured `ResumeArgs`, i.e. `--continue`.

```go
if len(usable) == 0 {
    if sawRecorded {
        return freshClaudeSession(p, pane, "no usable recorded session")
    }
    return p.Persistence.ResumeArgs   // <- still --continue
}
```

That pane carries the same collision risk as the ones the PR fixed: if another
pane in the same CWD has a more recently touched transcript, `--continue`
attaches this pane to it.

## Risks

- Two panes in one project directory can still end up on one conversation when
  one of them has no recorded session — e.g. a pane whose hook never fired and
  whose `workspace.json` entry predates session tracking.
- The window is narrow in practice (a pane normally records a session within
  seconds of spawning, and `refreshPluginStateFromHooks` persists it at
  shutdown), which is why it was not fixed under the same PR.

## Suggested Solutions

1. **Always start fresh.** Drop `ResumeArgs` from this branch too, so a pane
   with no identity gets a new one. Simple and removes the class entirely, but
   changes behaviour for anyone relying on `--continue` picking up a
   conversation started outside Quil in that directory.
2. **Make it conditional.** Use `--continue` only when no other live pane shares
   the CWD, so it cannot collide. Preserves the convenience, costs a scan.
3. **Make it a plugin decision.** Let `claude-code.toml` express "start fresh"
   vs "continue" for the no-identity case, so the trade-off is the user's.

Option 1 is the smallest and matches the reasoning in the rest of the resume
path; it needs a call on whether `--continue`-into-an-external-conversation is a
behaviour anyone wants.
