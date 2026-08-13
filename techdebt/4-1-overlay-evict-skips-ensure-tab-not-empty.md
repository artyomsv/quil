# Overlay eviction does not call ensureTabNotEmpty

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/daemon/overlay.go` (`destroyOverlay`) vs `internal/daemon/daemon.go` (`handleDestroyPane`, `ensureTabNotEmpty`) |
| Found during | Final whole-branch review of the overlay-retention feature (PR #153) |
| Date | 2026-08-12 |

## Issue

Quil has three pane-destruction paths that reach a tab, and two of them call
`ensureTabNotEmpty` afterwards — the guard that destroys orphaned overlays and
spawns a replacement pane so a tab is never left with nothing renderable.

`destroyOverlay`, added for the retention policies, does not. It deletes the
pane, cleans up its artifacts, broadcasts and returns.

## Risks

Unreachable today. `ensureTabNotEmpty` exists for the case where a tab's last
NORMAL pane goes away, and it already destroys any overlay alongside it — so by
the time an overlay is the only pane left in a tab, that path has run and the
overlay is gone with it. An overlay cannot be a tab's sole pane in the shipped
code.

The exposure is future-shaped rather than present: a change that lets an overlay
outlive its tab's normal panes (an overlay-only tab, or reordering the
`ensureTabNotEmpty` call) would silently leave an empty tab behind, and the
symptom — a tab that renders nothing — reads as a layout bug rather than a
lifecycle one.

## Suggested Solutions

1. Call `ensureTabNotEmpty(tabID)` from `destroyOverlay` for symmetry with the
   other two destruction paths. It is idempotent and cheap, and it removes the
   need for the reader to reason about reachability.
2. Add a comment to `destroyOverlay` stating that the call is deliberately
   omitted because an overlay can never be a tab's last pane, so the invariant
   is documented where someone would look for it.

Either closes it; option 1 costs one line and survives future changes that
option 2 would not.
