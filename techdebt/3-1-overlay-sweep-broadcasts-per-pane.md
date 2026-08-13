# The overlay idle sweep broadcasts once per evicted pane

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Trivial |
| Location | `internal/daemon/overlay.go` (`sweepIdleOverlays` → `destroyOverlay`) |
| Found during | Final whole-branch review of the overlay-retention feature (PR #153) |
| Date | 2026-08-12 |

## Issue

`sweepIdleOverlays` collects the expired overlay ids and then calls
`destroyOverlay` for each one. `destroyOverlay` ends with `broadcastState()` +
`requestSnapshot()`, so evicting N overlays in one tick emits N full workspace
broadcasts back to back rather than one after the loop.

Each broadcast is a complete workspace-state frame — the same must-deliver
critical frame that a 33-tab workspace measures in tens of KB.

## Risks

This is the shape of the 2026-08-09 incident, where unconditional per-item
sends overflowed a client's 64-slot critical queue and made the TUI close its
own connection. It is not reachable in the shipped configuration: `max_live`
defaults to 5, so at most 5 overlays can exist and a sweep can evict at most 5
in one tick.

It becomes reachable when the cap is DISABLED (`max_live = 0`) and many
overlays accumulate — a user with dozens of tabs who has turned the cap off and
then detaches, which stamps every overlay hidden at once and expires them
together on the same tick.

## Suggested Solutions

1. Have `destroyOverlay` do the session delete + artifact cleanup only, and
   move the single `broadcastState()` / `requestSnapshot()` pair to the end of
   `sweepIdleOverlays` and `enforceOverlayCap`, firing once if anything was
   evicted. This matches how `releasePanes` callers already batch.
2. Leave the per-pane broadcast but coalesce at the transport, which is a
   larger change and duplicates work `requestSnapshot`'s debounce already does
   for snapshots.

Option 1 is a few lines and keeps the eviction paths honest about cost.
