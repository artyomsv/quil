# Daemon restore/dialog logic missing unit tests

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/daemon.go`, `internal/tui/dialog.go` |
| Found during | QA pass for M2 persistence feature branch |
| Date | 2026-03-14 |

## Issue

Several non-trivial functions added or changed in this PR have no direct unit test coverage:

**`internal/daemon/daemon.go`**
- `isValidHexID(id, prefix string) bool` — ID validation used by `restoreWorkspace()` to guard against corrupt snapshot data. The logic (prefix match + 8 hex chars) is easy to get wrong at boundaries.
- `buildWorkspaceState()` — serializes the full session to a `map[string]any`. No test verifies the key names, layout embedding, or pane name omission logic.
- `snapshotDebounced()` — debounce path (TOCTOU-safe via lock). The 1s boundary behaviour is not tested.

**`internal/tui/dialog.go`**
- `handleConfirmKey()` — confirm/cancel logic for destructive pane/tab operations. The "y" path sends IPC messages; no test covers the state machine transitions.
- `handleSettingsKey()` — bool toggle vs string edit branching, including the `dialogEdit` mode transitions.
- `settingsFields()` — get/set closures mutate `Model.cfg` fields; not exercised by any test.

## Risks

- A bug in `isValidHexID` could silently skip restoring valid panes (off-by-one on prefix length) or accept IDs with uppercase hex (which UUIDs never produce but a corrupted file might).
- `buildWorkspaceState()` is the single serialisation path for snapshots and IPC broadcasts — a key-name typo would break all clients silently.
- Dialog state-machine bugs are invisible until manual testing; a missed `m.dialog = dialogNone` reset could trap the user in a dialog.

## Suggested Solutions

1. **`isValidHexID`**: add a table-driven test in `internal/daemon/daemon_test.go` covering valid IDs, wrong prefix, wrong length, uppercase hex, and empty string.
2. **`buildWorkspaceState`**: add a test that creates a known session via `SessionManager`, calls `buildWorkspaceState()`, and asserts key presence and values — including the optional `layout` and `name` fields.
3. **Dialog state machine**: the TUI package already has `layout_test.go` using plain Go (no TTY). Add `dialog_test.go` following the same pattern — construct a `Model` with a known `cfg`, send `tea.KeyMsg` values through `handleDialogKey`, and assert `m.dialog` and `m.cfg` fields.
