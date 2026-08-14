# QA Agent Memory Index

## Project
- [project_test_tooling.md](project_test_tooling.md) — Docker-based test runner (`./scripts/dev.sh test`), no local Go, Windows PTY untested in CI
- [project_ipc_write_window_wiring_gap.md](project_ipc_write_window_wiring_gap.md) — newConn → writeDeadline wiring is pinned by TestNewConn_UsesTheProductionWriteWindow; found by mutation, was uncovered
- [project_notify_wiring_gap.md](project_notify_wiring_gap.md) — raiseAttentionToast/sweepOutstandingToasts call sites were mutation-removable with a green suite; now pinned by Update-driven tests
