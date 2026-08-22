# MCP reports a worktree placeholder as a dead pane

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Small |
| Location | `internal/daemon/daemon.go` — `buildPaneInfos`, `buildPaneStatus` |
| Found during | Code review of PR #185 (worktree preparation) |
| Date | 2026-08-23 |

## Issue

A pane waiting on `git worktree add` has no child process by design
(`Pane.PreparingWorktree`, PR #185). The MCP surface has no notion of that state,
so `list_panes` and `get_pane_status` report it as:

```
running: false, pending: false, exit_code: null
```

which is indistinguishable from a pane whose process died. The TUI renders a
spinner and the branch name; an agent driving the same daemon over MCP sees a
corpse.

`send_to_pane` against it also drops the input on the nil PTY (`handlePaneInput`
returns early) while the tool reports success.

## Risks

An agent reads "not running, never exited" as crashed and calls `restart_pane`.
That is now **refused** — PR #185 guards `handleRestartPaneReq` on
`PreparingWorktree` precisely because restarting a placeholder spawned a shell
behind the spinner and orphaned its PTY child — so the outcome is confusing
rather than dangerous.

What remains is that an agent has no way to learn *why* the pane is not running,
and a silently-dropped `send_to_pane` reads as a delivered keystroke.

## Suggested Solutions

1. Carry the branch on `PaneInfo` / `PaneStatusRespPayload` (it is already on the
   workspace broadcast as `preparing_worktree`) so the state is self-describing
   over MCP as well as in the TUI.
2. Make `send_to_pane` report a failure when the target has no PTY, rather than
   succeeding into nothing. Wider than this feature — it is true of every
   PTY-less pane, including the `SpawnError` ones that have existed since
   stage B.
