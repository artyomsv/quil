# QA Agent Memory Index

## Project
- [project_ipc_write_window_wiring_gap.md](project_ipc_write_window_wiring_gap.md) — newConn → writeDeadline wiring is pinned by TestNewConn_UsesTheProductionWriteWindow; found by mutation, was uncovered
- [project_notify_wiring_gap.md](project_notify_wiring_gap.md) — raiseAttentionToast/sweepOutstandingToasts call sites were mutation-removable with a green suite; now pinned by Update-driven tests
- [project_test_tooling.md](project_test_tooling.md) — Docker test runner, no local Go, worktree isolation recipe, mutation-testing gate booleans/dispose calls, concurrent agents' stray zz_probe test files

## Feedback
- [feedback_tier_seam_and_hardcoded_dispatch_gaps.md](feedback_tier_seam_and_hardcoded_dispatch_gaps.md) — dispatch-registry refactors: hardcoded key switches outside the table, case arms moved across a tier seam, and nil-safe fallbacks all evade naive test-presence checks; mutate and re-run to confirm
