# The overlay cap may evict a visible overlay; the idle sweep may not

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/daemon/overlay.go` (`enforceOverlayCap` vs `sweepIdleOverlays`) |
| Found during | Final whole-branch review of the overlay-retention feature (PR #153) |
| Date | 2026-08-12 |

## Issue

The two retention policies disagree about visible overlays, and nothing says so.

`sweepIdleOverlays` deliberately refuses to evict an overlay that is on screen —
that refusal is the whole reason visibility is reported by the client rather
than inferred from pane activity, and `TestSweepIdleOverlays_NeverEvictsAVisibleOverlay`
pins it.

`enforceOverlayCap` has no such guard. It sorts every live overlay by
`OverlayShownAt` and evicts the least recently shown, whether or not one of them
is currently displayed.

## Risks

Low in practice, because only one overlay can be on screen at a time (only the
active tab renders) and that one has just been shown — so it sorts LAST in the
LRU order and is the last candidate the cap would ever reach. Reaching it
requires the cap to evict every other overlay in the same call, which means
`max_live` was lowered below the live count while the user was looking at one.

The real cost today is that the asymmetry is undocumented: a reader who has just
absorbed "a visible overlay is never evicted" from the sweep will reasonably
assume the cap honours the same rule.

## Suggested Solutions

1. Document the asymmetry in `enforceOverlayCap`'s comment and say why it is
   acceptable (the visible overlay is by construction the most recently shown).
2. Make the cap skip visible overlays outright — cheap, but it means a cap can
   fail to make room, so the caller needs a defined behaviour for "nothing
   evictable" (admit anyway, or refuse the overlay).
3. Leave as-is.

Option 1 is probably right: the guarantee already holds by construction, and
option 2 introduces a failure mode the current design does not have.
