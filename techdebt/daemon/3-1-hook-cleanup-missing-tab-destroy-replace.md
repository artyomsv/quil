# Hook-event teardown skipped on tab destroy and pane replace

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Trivial |
| Location | `internal/daemon/daemon.go:893-914` (handleDestroyTab), `:1029-1052` (handleReplacePane); correct examples at `:1075-1085`, `:2929-2934` |
| Found during | Whole-project memory-leak audit |
| Date | 2026-06-10 |

## Issue

Only `handleDestroyPane`/`handleDestroyPaneReq` call
`hookSpool.Cleanup(paneID)` + `hookIngester.Cancel(paneID)`. `handleDestroyTab`
destroys all panes in the tab and `handleReplacePane` deletes the old pane
with **no** hook cleanup. Leaked per pane in a long-running daemon:

- `Spool.offsets` + `Spool.parseErrCounts` map entries (`hookevents/spool.go:55-57`)
- `Ingester.rates` entry (~2.4 KB of timestamps, `ingest.go:50`)
- the on-disk `$QUIL_HOME/events/<paneID>.jsonl` spool file, which
  `Spool.Tick` then re-stats/opens every 200 ms forever

Related cleanup gaps with the same fix location:

- `Spool.Cleanup` itself never deletes `parseErrCounts` even on the clean path
- `$QUIL_HOME/sessions/<paneID>.id` and `sessions/opencode-<paneID>.id` are
  never unlinked on any destroy path (disk-only, grows monotonically)

## Risks

Slow daemon memory growth + a 200 ms polling tax per leaked spool file; users
who churn claude-code/opencode panes via tab close accumulate this daily.

## Suggested Solutions

Extract one `cleanupPaneArtifacts(paneID)` helper (spool cleanup incl.
parseErrCounts, ingester cancel, session-id file unlink) and call it from all
four destroy/replace paths.
