# update state.json writes through a fixed temp path

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/update/state.go:74` (`saveJSON`) |
| Found during | security review of PR #160 (re-check before applying a staged update) |
| Date | 2026-08-15 |

## Issue

`saveJSON` writes atomically via temp+rename, but the temp path is a **fixed**
`<path>.tmp`:

```go
tmp := path + ".tmp"
if err := os.WriteFile(tmp, data, 0600); err != nil { ... }
if err := os.Rename(tmp, path); err != nil { ... }
```

Two concurrent savers open that same path with `O_TRUNC` on separate
descriptors and both write from offset 0. A shorter write leaves the longer
writer's tail in place, so the file that gets renamed into position can be
invalid JSON. `LoadState` swallows a parse error and returns the zero `State`
(state.go:25-34), so the failure is **silent**: the staged/latest announcement
disappears and `LastCheckMs` resets.

The same helper backs the TUI-owned `notified.json`, which is written by a
different process than the daemon-owned `state.json` — a cross-process race no
in-process mutex can cover.

**Widened 2026-08-16 (What's New, PR #165).** `saveJSON` now has a third
caller: `lastrun.json`, written by `SaveLastRunVersion` from `resolveWhatsNew`
during `NewModel` — i.e. on every TUI startup, unconditionally, before anything
else happens. Two TUIs launched together therefore race on `lastrun.json.tmp`.
The consequence stays Low and is self-healing in the same way: a corrupt marker
makes `LoadLastRunVersion` return `""`, which `resolveWhatsNew` reads as a fresh
install, so one launch shows no What's New dialog and the marker is rewritten
correctly on the spot. Noted because the new writer fires far more often than
the two existing ones.

## Risks

Silent loss of the update announcement (the row goes back to "up to date" when
an update is in fact staged), and a reset `LastCheckMs`. Low, not medium,
because the pipeline is self-healing here: the next press re-checks and answers
`AlreadyStaged` from the manifest on disk without downloading anything, so the
worst case is one confusing dialog state rather than a lost or wrong update.

## Current mitigation (why this is Low and not Medium)

PR #160 closed the in-process half of it. The daemon now has three writers of
`state.json` — the daily tick, the check-only refresh, and the on-demand stage —
and all three go through `Daemon.mutateUpdateState`, which serialises the whole
read-modify-write under `updateStateMu`; `handleStageUpdateReq` additionally
took its own single-flight slot. So the "single writer per file" the pipeline is
designed around holds again *within* the daemon. What is left is the generic
helper being unsafe if any future caller writes concurrently from another
process.

## Suggested Solutions

1. **Preferred:** `os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")`
   so every writer gets its own file, then rename. Three lines, no API change,
   removes the shared-path collision entirely.
2. Leave as is and rely on the callers keeping the single-writer discipline —
   viable but undocumented at the helper, which is exactly how it was reached.
