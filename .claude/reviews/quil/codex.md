# Code Review State: quil / codex

Last reviewed: 2026-09-04
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [rules/H1 = code-quality/1] `cleanupPaneArtifacts` removes `codex-<paneID>.id` (+ seeded in `TestCleanupPaneArtifacts_RemovesAll`) — round 1
- [code-quality/2] `resumeTemplateFor` codex arm dispatches on the plugin NAME alone, so `sessions = "claude"` + `strategy = "preassign_id"` cannot reach the claude arm (test covers both strategies) — round 1
- [code-quality/3] hook timeouts written as 15 s (SessionEnd 3 s) instead of codex's 600 s default; hash covers the timeout — round 1
- [security/L1] `cmdMetaChars` includes the cmd.exe delimiters `; , =` — round 1; superseded in round 2: the hook command names `%QUIL_HOOK_EXE%` and carries no path at all (Greptile P1)
- [security/L2] `hookLog` strips control characters (`stripControl`) from codex-supplied strings — round 1
- [security/L3] `readRolloutUsage` Lstat + regular-file check before Open — round 1
- [code-quality/S1] `claudeHookExeFn` renamed `quildExeFn` (both hook preps read it) — round 1
- [code-quality/S2] Unix `hookCommandFor` reports `" \ $ \`` in the exe path with a note — round 1
- [code-quality/S3] `truncate` returns "" for a cap below the ellipsis — round 1
- [code-quality/S4] fresh-start rows of `TestResolveSpawnArgs_CodexResume` assert no `session_id` is left in PluginState — round 1
- [code-quality/S5] `session_test.go` asserts the recorded path verbatim (no hand-rolled HasSuffix, no GOOS-guarded assertion) — round 1
- [code-quality/S6] MCP `create_pane` type description names `opencode` and `codex` — round 1
- [rules/M2] techdebt `4-2-hook-log-unbounded-and-oversized-hook-stdin.md` Location names `internal/codexhook/` — round 1
- [rules/M4] `site/src/data/plugins.ts` has a codex entry; feature lists / matrices / page blurbs name Codex — round 1
- [rules/L6] `TestDefaultPlugins_AgentPluginsShipWideCanvas` covers codex — round 1
- [qa/1] `TestSpawnPane_CodexArmInjectsHookOverride` + `TestSpawnPane_CodexRestoreResumesRecordedSession` pin the spawnPane wiring — round 1
- [qa/2] `TestDaemon_RefreshPluginStateFromHooks` has a codex pane; `saveHookStubs` saves `readCodexSessionFn` — round 1
- [qa/3] `TestEmitHookEvent_CodexTierGate` covers the daemon-side `[notification.hooks] codex` gate — round 1
- [qa/4] `TestResolveSpawnArgs_CodexResume_ClearsStaleTranscriptPath` — round 1
- [qa/5] `TestModelUsageData_RetriesUntilTheRolloutLine` via the `readRolloutUsageFn` seam — round 1

## Round 2 (2026-09-04, on 33a3139) — the four review agents stopped with "You've hit your session limit · resets 3am (Europe/Zurich)" and delivered no report; the Greptile PR bot's findings were taken instead
- [greptile/P1] Windows hook command still fell back to a quoted path when no 8.3 name exists → the command now names `%QUIL_HOOK_EXE%` (`"$QUIL_HOOK_EXE"` on Unix) and the path travels in the pane env; verified with codex 0.146.0 running quild from a directory with a space — round 2
- [greptile/P2] `TestRouterRemovedPumpPublishesNothing` lost coverage of the publication boundary → new `blocked publish` subtest fills the inbox first so the pump is parked in its select when removed — round 2

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/M3] docs landed in a follow-up commit on the branch — the repo squash-merges, so the merged commit carries code and docs together (round 1)
- [rules/L5] branch prefix `feat/` vs documented `feature/` — cosmetic; the branch name is discarded at squash (round 1)
- [rules/L7] PR size (4,820 insertions) — 2,587 lines are the design spec and implementation plan (round 1)
- [code-quality/S7] `hook.log` has no rotation and the Stop breadcrumb fires per turn — pre-existing in claudehook, tracked as techdebt 4-2 (entry updated to name codexhook) (round 1)
- [qa] `runCodexHook` (cmd/quild/hook.go) has no direct test — matches `runClaudeHook`; the subcommand is a thin env-to-struct shim over the tested `RunHook` (round 1)
