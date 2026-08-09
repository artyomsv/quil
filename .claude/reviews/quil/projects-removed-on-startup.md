# Code Review State: quil / projects-removed-on-startup

Last reviewed: 2026-08-09
Rounds completed: 2

## Resolved (fixed in code; do not re-raise)

- [rules/HIGH] `disconnectDest`'s new cache-file removal reached the owner's real `~/.quil/` from two tests that lacked a `QUIL_HOME` guard (`dialdest_test.go`, `dialdest_race_test.go`) — both now set `t.Setenv("QUIL_HOME", t.TempDir())` — round 1
- [qa/IMPORTANT] the version gate's SELECTION (`gate := old == nil` in `redialRemote`) was untested at its call site; inverting it broke no test — `TestRedialRemote_GateSelection` added, verified non-vacuous by inversion — round 1
- [rules/MEDIUM] the new `~/.quil/remote-projects-*.json` state file was undocumented for users — added to `docs/configuration.md` beside `recent-cwds.json` — round 1
- [security/LOW] `LoadRemoteProjects` read remote-originated JSON with no cap on entry count or field length — `maxCachedProjects` / `maxCachedFieldLen` added, over-long values truncated silently — round 1
- [whole-branch/IMPORTANT] selecting an offline project froze the entire TUI: `freezeInput` swallowed every key and mouse event while the ladder held `active`, so only the quit binding escaped — exemption added for a current project with `Offline != nil`, placed below the `clipboardPastedMsg` branch — pre-PR
- [whole-branch/MINOR] `offlineParked` was declared and `laddered()` returned true for it while nothing assigned it — constant removed, `laddered()` reduced to `k == offlineRetrying` — pre-PR
- [whole-branch/MINOR] Ctrl+T and the palette's New tab falsely refused when only offline rows existed (the pre-first-broadcast window) — both now allow the action when `onlyOfflineProjects()` — pre-PR
- [whole-branch/MINOR] an offline row could become the active project at launch via `indexOfProject`'s return-0-on-miss — `resolveActiveProjectIndex` prefers a live project — pre-PR
- [whole-branch/MINOR] `disconnectDest` leaked `offlineWoken` / `cachedRemote` entries and the on-disk cache file — all three now dropped — pre-PR
- [whole-branch/MINOR] `.claude/rules/remote-transport.md` carried a now-false "Known gap … needs a relaunch" paragraph and the pre-rename `defaultConnectTimeout` — both corrected — pre-PR
- [task/CRITICAL] the new `cacheRemoteProjects` call site made four pre-existing `project_test.go` tests write into the real `~/.quil/`, invisible because `dev.sh test` runs in `docker run --rm` with a throwaway `/root` — guards added — pre-PR
- [task/IMPORTANT] `TestSendUpdateProject_RefusesOfflineProject` was vacuous: the router dropped the message for an unregistered dest regardless of the guard — the dest is now registered so the guard is what stops the send — pre-PR

### Round 2

- [security/HIGH] the launch warning in `cmd/quil/main.go` printed `o.err` with `%v`, and a version-mismatch error interpolates the daemon-reported version verbatim — `version.Parsed` strips everything after the first `-` before validating, so `0.0.1-<escape bytes>` parses cleanly and reached the operator's terminal unbounded. A REGRESSION this branch introduced: master printed a constant string. Fixed by `transport.SanitizeForTerminalMessage` at the print, which also strips bidi controls `sanitizeForTerminal` did not — round 2
- [security/MEDIUM] the cache WRITE path was uncapped (only the read was capped in round 1), so a daemon reporting a huge project name wrote a huge file, and per-broadcast field churn defeated the change detection; `LoadRemoteProjects` also materialised an oversized file via bare `os.ReadFile` before any cap applied — caps moved to where the list is built, read bounded by `io.LimitReader` — round 2
- [security/LOW] `CachedProject.ID` was the one remote-sourced field left untruncated — round 2
- [security/LOW] `SeedOfflineDest` filtered synthetic IDs but not `proj-offline@` ones, so a daemon on host A could report an ID colliding with host B's synthesised row and steal launch focus — round 2
- [security/LOW] production-`~/.quil` isolation in tests held only because every unguarded test happened to pass `dest == ""` — a property of the arguments, not the code. A `TestMain` now defaults `QUIL_HOME` to a temp dir — round 2
- [rules/HIGH] no `CHANGELOG.md` entry; CI's changelog gate fails any PR whose changes reach the binary — added under `## [Unreleased]` — round 2
- [qa/MEDIUM] `truncateBytes`' rune-safety was unverified: the only test fed it ASCII, so deleting the rune-boundary backoff passed — multi-byte test added, confirmed non-vacuous — round 2
- [qa/MEDIUM] `wakeOfflineDests`' once-only guarantee was pinned at the function, not at its only call site in `Update`'s `WindowSizeMsg` arm — test now drives `Update` twice. Third instance on this branch of testing a decision function while its call site makes the decision unreachable — round 2
- [qa/LOW] the all-synthetic cache case never drove `cacheRemoteProjects`' empty-list early return — case added — round 2

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)

- (none)

## Notes for a future round

- The `code-reviewer` agent produced no report in either round (six silent idles). That dimension rests on the 12 per-task reviews, the whole-branch pass, and a controller-run inspection of `offline.go` / `remoteprojects.go` / the `freezeInput` exemption — which found nothing of substance. Treat it as the least-covered angle if a round 3 happens.
- Latent, not worth fixing now: `SeedOfflineDest` ends with `append(kept, rows...)`, so re-seeding a destination moves its rows to the end of the sidebar. Unreachable today — reclassification mutates `Offline.Kind` in place and launch seeds each host once.
