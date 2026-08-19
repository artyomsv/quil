# Restore can replay a torn alt-screen snapshot for ssh and stripe panes

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/daemon/daemon.go` (`handleAttach`), `internal/daemon/session.go` (`MouseModes`) |
| Found during | Code review of PR #173 (issue #172) |
| Date | 2026-08-19 |

## Issue

PR #173 stops the daemon replaying a ring buffer into a pane whose child is on the alternate screen, because such a stream carries frame deltas rather than history. The decision reads `pane.MouseModes.altScreen`, which the daemon learns by scanning the pane's own output.

`MouseModes` is deliberately **not persisted** — it starts at the zero value after a daemon restart and only becomes true once *this* incarnation's child re-emits its enable sequence. On the FIRST attach after a restore, therefore, the daemon cannot know the pane is on the alternate screen, and the `ghostsnap` replay proceeds using the snapshot loaded from disk.

For `claude-code` that is harmless: `restoresOwnHistory` (strategy `preassign_id`) already skips the restore replay for a different reason. But `ssh` and `stripe` are `ghost_buffer = true` with strategy `rerun` and no such skip. A pane running `vim`, `less` or `htop` over ssh when the daemon restarts has its torn alt-screen snapshot replayed on the first attach — the exact symptom #172 fixes on the live-reconnect path.

## Why it was deferred

It is pre-existing behaviour rather than a regression: before #173 that replay happened too, and #173 does not make it worse. Closing it needs a real design decision rather than a patch, and the options are not obviously ranked:

- **Persist the alt-screen bit** with the snapshot. Cheapest to reason about, but `MouseModes` is documented as broadcast-not-persisted, and adding a field to the workspace format for it invites the question of what else should be persisted.
- **Track "never scanned this incarnation"** (`everScanned`) and treat an unscanned pane conservatively. But conservative in which direction? Skipping the replay for every freshly restored pane would break the common, correct case — a `terminal` pane replaying its history after a restart, which is the feature working as intended.
- **Sniff the snapshot itself** for an unbalanced `?1049h` when loading it from disk. Self-contained and needs no format change, but it is a second parser over the same bytes and it inherits the ring-wrap problem: a snapshot that begins after the enable looks main-screen.

## What to do

Decide between the three above, with the same method PR #173 used to settle its own premise: capture a real `ssh`-hosted full-screen pane, restart the daemon, and replay the loaded snapshot through the client emulator (`PaneModel.AppendOutput` + `vtRow`) to see what the grid actually contains. If it is torn, the fix is worth the format change; if the `rerun` respawn repaints anyway — plausible, since `rerun` re-executes the command — this may need nothing but a comment.
