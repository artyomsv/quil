# config.Save still writes the legacy [keybindings] table

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Trivial |
| Location | `internal/config/config.go` (`Save`), `internal/config/bindings.go` |
| Found during | Implementation of key sequences + binding presets |
| Date | 2026-08-16 |

## Issue

`bindings.toml` is the source of truth for keybindings, but `config.Save` still
serializes the full `[keybindings]` table into `config.toml`. Nothing reads it
after the one-way migration has run, so every write carries 42 dead fields that
a user editing `config.toml` by hand will reasonably assume still work.

This is the deliberate first half of a two-release rollout, not an oversight.

## Why it was deferred

`Mutate` is Load + fn + Save (`config.go:546`), so the first unrelated config
mutation after upgrade — a remote install writing `[remote.hosts]`, say —
deletes whatever `Save` stops emitting.

Quil ships auto-update with a real rollback path (`internal/update/`,
rename-aside swap). A rollback lands a binary that predates `bindings.toml` and
cannot read it. If the legacy table has already been stripped by then, that
user's entire keymap silently resets to defaults, with the customizations
sitting in a file the running binary does not know exists.

Release N — the PR that added `bindings.toml` — therefore keeps writing the
legacy table so a rollback still finds the keys. This entry is release N+1.

## Fix

Strip `[keybindings]` from `Save` **iff `bindings.toml` exists on disk**. The
guard is load-bearing: if the file is missing — a failed write, or the user
deleted it to reset — the legacy table must survive, because it is the
migration source and `MigrateBindings` will read it again on the next launch.

`TestSave_StillEmitsLegacyKeybindings` (`internal/config/bindings_test.go`)
pins the release-N behaviour. Inverting that test is the first step of this
change, and it should become two cases: stripped when `bindings.toml` exists,
preserved when it does not.

## Risks of leaving it

Low and cosmetic rather than corrupting. The dead table is confusing to anyone
reading `config.toml`, and it will keep round-tripping stale values forever —
including values the user changed in `bindings.toml`, which makes the two files
disagree on screen while only one of them is consulted.
