# TestNewTab_OnlyOfflineProjectsStillReachesTheLocalDaemon does not defend the unstamped-send branch

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `internal/tui/new_tab_pane_test.go` (`TestNewTab_OnlyOfflineProjectsStillReachesTheLocalDaemon`) |
| Found during | Code review of PR #191 (new-tab picker Esc cancels) — pre-existing, not introduced there |
| Date | 2026-08-25 |

## Issue

The two startup-window tests read as a pair guarding the same invariant:
`sendCreateTab` sends UNSTAMPED when `createPaneDest == ""`, because `stampDest`
maps `""` to `destLocal` and that makes `Router.Send`'s sole-conn fallback —
gated on `!stamped` — unreachable. Under `--remote` the router holds no `""`
conn at all, so a stamped send is dropped against a conn that never existed.

Only the FIRST of the pair actually defends it. Mutating `sendCreateTab` to
stamp unconditionally (`if false && dest == ""`) gives:

```
TestNewTab_BeforeTheFirstBroadcastReachesTheSoleDaemon    FAIL
TestNewTab_OnlyOfflineProjectsStillReachesTheLocalDaemon  PASS
```

`OnlyOfflineProjects` builds its router with a `""`-keyed conn
(`NewRouter(map[string]Client{"": local})`), so a stamped send still routes to
it and the mutation survives. The test is a real guard for what it was written
for — the offline-projects carve-out in `handleNewTab`, i.e. that a workspace of
unreachable stand-ins does not make Quil refuse a local create — but it says
nothing about stamping.

Verified against `origin/master` as well as the PR branch: identical results, so
this is not a regression from re-pointing the tests off the step-0 Esc.

## Why it matters

Two tests that appear to cover an invariant, one of which cannot fail on it, is
worse than one honest test: a future change to the stamping rule gets a single
red test and reads as a flake or a narrow edge case rather than as the
load-bearing invariant it is.

## Fix

Either key the offline test's router by a host name so the `""` conn genuinely
does not exist (matching the `--remote` shape the invariant is about), or add a
comment stating plainly that this test guards the carve-out and NOT the
stamping, so the pair is not over-trusted. The first is preferable and is a
one-line change to the `NewRouter` literal — but check it still exercises the
carve-out afterwards, since that is what the test is named for.
