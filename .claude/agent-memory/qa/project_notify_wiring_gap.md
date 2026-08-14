---
name: project_notify_wiring_gap
description: raiseAttentionToast's real call site (applyWorkTransition) is never exercised by any test — same bypass shape as [[project_ipc_write_window_wiring_gap]]
metadata:
  type: project
---

`internal/tui/workstate.go`'s `applyWorkTransition` captures `wasBlocked`/`wasUnseen`
beside `wasWorking` (before the state-mutating switch) and, at the bottom, calls
`m.raiseAttentionToast(pane, proj, wasBlocked, wasUnseen)` — this is the ONLY
production call site of `raiseAttentionToast` (system-notifications feature,
round 1, 2026-08-13).

Every test in `internal/tui/notify_test.go` (`TestRaiseAttentionToast_*`,
`TestSweepOutstandingToasts_*`) calls `m.raiseAttentionToast(...)` DIRECTLY with
hand-picked `wasBlocked`/`wasUnseen` args. `workstate_test.go` has ~60
`TestApplyWorkTransition_*` tests covering the blockedSince/unseen state
machine exhaustively, but zero of them install a `desktopNotifier` (fake) or
assert anything about a toast — confirmed via grep, no match for
`SetDesktopNotifier|fakeNotifier|notifier.sent` in workstate_test.go.

**The gap**: if the wiring line in `applyWorkTransition` were deleted, or the
`wasBlocked`/`wasUnseen` capture were moved to AFTER the switch (which would
break edge detection — before/after would always read equal), or `proj` were
swapped for a different project, no test would fail. This is the exact shape
of [[project_ipc_write_window_wiring_gap]]: a seam added for testability
(direct-call access to `raiseAttentionToast`) becomes the only path any test
uses, so the real call site goes uncovered.

**Also found while reviewing this feature (minor, same file)**:
- `TestRaiseAttentionToast_Gates` covers `cfg.Blocked = false` (blocked kind
  off → 0 sent) but has no symmetric case for `cfg.Done = false` with
  `pane.unseen` true — the done-kind gate in the `switch` is untested.
- `fakeNotifier.err` field exists but is never set non-nil in any test — the
  `logger.Debug` error paths in both `raiseAttentionToast`'s `Notify` call and
  `sweepOutstandingToasts`'s `Withdraw` call are dead code as far as tests go.
- `MaxFieldRunes = 120` in `internal/notify/toastxml.go` has no test pinning
  the exact truncation boundary — only a loose "under 4096 bytes total"
  bound and a rune-safety check. A change to the constant would not fail CI
  unless pushed to an extreme.

**How to apply**: when reviewing this feature in a later round, check whether
a test now sends a real pane event through `Update()`/`applyWorkTransition`
with a fake notifier installed and asserts on `fakeNotifier.sent` — that would
close the primary gap. Prefer this shape generally: whenever a test file
directly calls an unexported method that has exactly one production call site
elsewhere in the package, ask whether anything still exercises that call site
itself, not just the method.

## RESOLVED (2026-08-13, same session)

Closed in commit `5ce7944`. `internal/tui/notify_test.go` now drives real
`paneEventMsg` events through `Model.Update` with a fake notifier installed:
`TestUpdate_PermissionRequestRaisesToast`, `TestUpdate_FinishedTurnRaisesToast`
and `TestUpdate_SweepWithdrawsAfterTheUserAnswers`.

Verified by re-running the mutation: deleting BOTH call sites now fails all
three, where previously the whole suite stayed green.
