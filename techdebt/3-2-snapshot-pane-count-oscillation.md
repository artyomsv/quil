# Daemon snapshotter reports oscillating pane counts

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/` — snapshot/requestSnapshot path + 30s periodic safety-net timer |
| Found during | Investigation of TUI freeze on 2026-04-22 |
| Date | 2026-04-22 |

## Issue

The daemon emits two streams of `snapshot: … tabs, … panes, …` log lines at offset cadences (roughly 30 s apart, but ~8 s shifted from each other). Before a specific pane is created they agree; afterwards they disagree by one.

Concrete evidence from `~/.quil/quild.log` on 2026-04-22:

```
11:48:26.962 snapshot: 6 tabs, 7 panes, 0 buffers, took 4ms   [cadence A]
11:48:34.615 snapshot: 6 tabs, 7 panes, 0 buffers, took 4ms   [cadence B]
11:48:38.041 ipc recv: create_pane
11:48:38.042 pane created: pane-3a2ff76b
11:48:38.591 snapshot: 6 tabs, 8 panes, took 5ms              [debounced snapshot after create]
11:48:56.962 snapshot: 6 tabs, 8 panes, took 4ms              [cadence A → 8]
11:49:04.617 snapshot: 6 tabs, 7 panes, took 4ms              [cadence B → 7]
11:49:26.963 snapshot: 6 tabs, 8 panes                         [cadence A]
11:49:34.615 snapshot: 6 tabs, 7 panes                         [cadence B]
… continues alternating …
```

Cadence A (`:XX:26/:56`) consistently reports **8 panes**. Cadence B (`:XX:04/:34`) consistently reports **7 panes**. Both come from the same daemon process; the logs are from the same `quild.log` file.

Per CLAUDE.md ("Snapshot triggers"), the daemon has two snapshot paths:
1. A debounced/centralized event queue via `snapshotCh` (`requestSnapshot()` → `Wait()` loop with 500 ms `time.AfterFunc`).
2. A periodic 30-second timer as safety net.

One plausible interpretation: one path iterates `tab.Panes` (the slice of pane IDs stored on the Tab) and the other iterates the pane map by looking up tab membership — and the two are out of sync after a pane create. Another: one path holds the session-manager lock, the other reads a cached view that isn't invalidated on every mutation. A third: the snapshotters observe state at different points relative to the `tab.Panes = append(...)` in the create-pane handler (non-atomic append visible mid-mutation to one reader but not the other).

## Risks

- Restored workspace on daemon restart can silently drop a pane if the periodic-cadence snapshot happened to persist the "stale" view.
- Any MCP consumer reading `list_panes` via the snapshot path could see a different count than the live daemon knows about.
- Masks more serious concurrency bugs — the "visible" symptom (pane count wobbles by one) is easy to hand-wave but the underlying race has no upper bound on the class of inconsistency.

## Reproduction

1. Launch the dev TUI (`./scripts/quil-dev.ps1` or `./scripts/quil-dev.sh`) and confirm the `[dev]` indicator.
2. Create at least one new pane (any type) via Ctrl+N. The debounced snapshot fires at create time and the two cadences realign to the new count immediately after.
3. Wait through two full 30 s cycles (~60 s).
4. `grep "snapshot:" .quil/quild.log | tail -20` and look for adjacent lines reporting different `N panes` totals from the same tab count.

Baseline (before create) should show identical pane counts on both cadences; post-create they diverge by one and stay that way until the next pane mutation.

## Suggested Solutions

1. **Audit both snapshotters for the locking contract.** Which snapshot path reads which state under which mutex? Ensure both go through the same `sessionManager.Snapshot()` (or equivalent) that takes the manager lock and builds the payload atomically.
2. **Eliminate cadence duplication** — if the periodic 30 s timer exists solely as a safety net and the debounced path is authoritative, the periodic one can simply call `requestSnapshot()` instead of building its own payload.
3. **Add an integration test** (`//go:build integration`) that creates a pane, triggers both snapshot paths, and asserts identical pane counts across a few seconds.

Investigation must precede any fix — reproducing the oscillation is step 1 so we can confirm the root cause rather than guess.

## Out of scope

Not related to the 2026-04-22 TUI freeze at `apply: new pane preparing`. That freeze is a TUI-side Update wedge being investigated via the watchdog + breadcrumb logging added in the same PR.
