# A spinner tick landing on a detached leaf leaves spinnerTickRunning stuck true

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/tui/model.go:2058` (`spinnerTickMsg` arm) |
| Found during | Code review of PR #185 (worktree preparation) |
| Date | 2026-08-23 |

## Issue

The `spinnerTickMsg` arm resolves its target and gives up when it cannot:

```go
pane := m.spinnerTargetPane(msg.paneID)
if pane == nil {
    return m, nil
}
```

`spinnerTargetPane` resolves only through `tab.Root.FindLeaf` / `overlayPane`, so
a tick that lands while the pane's leaf is **detached from the tree** finds
nothing and the chain ends — **without clearing that pane's
`spinnerTickRunning`**. Every other exit from the arm clears it.

If the same `PaneModel` is later re-inserted (it comes back through
`existingPanes` rather than `newPaneIDs`), the re-enrol guard is
`if pane.spinnerTickRunning { continue }` — so the flag is stuck true with no
live chain behind it, and the pane's spinner is frozen for the rest of its life.

## Risks

A frozen glyph in front of work that is still happening — the same
confidently-wrong signal the worktree placeholder exists to remove, in a
different pane state.

**Not reachable from the new-tab worktree placeholder**, which is why PR #185 did
not fix it: that path arms neither `pendingSplit` nor `worktreeReplaced`, so its
leaf never leaves the tree. It is reachable in principle through those two
detach paths (a worktree REPLACE holds a detached pane in `worktreeReplaced` for
the length of a checkout, which is exactly when ticks are flying).

Pre-existing: the arm has had this shape since the restore spinner was added.
PR #185 did not introduce it and deliberately left the shared spinner machinery
alone rather than widening a worktree PR into it.

## Suggested Solutions

1. Clear the flag on the nil branch. It needs a lookup that sees detached panes —
   `spinnerTargetPane` cannot, by construction — so either widen that helper or
   consult `m.worktreeReplaced` alongside it.
2. Alternatively, drop `spinnerTickRunning` as a `PaneModel` field and key the
   live chains off a `map[string]bool` on `Model`, which can be cleared without
   resolving the pane at all.
