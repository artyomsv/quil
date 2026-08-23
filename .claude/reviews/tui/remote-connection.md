# Code Review State: tui / remote-connection

Last reviewed: 2026-08-23
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/HIGH-1] private IP + ssh username in a tracked fixture — `sidebar_offline_test.go` now uses the `artyom@gpu01` placeholder, and the commit was amended so the blob never carried it — round 1
- [security/MEDIUM-2] project name rendered uncapped; `lipgloss.Place` pads but never clips — every line of the empty-tab area is capped, offline and live branches alike, with a frame-width regression test carrying a non-vacuity guard — round 1
- [security/MEDIUM-3] daemon-reported version unbounded (10 MB IPC frame the only ceiling; `version.Parsed` truncates at the first `-`) and re-measured every frame — clamped at the handshake door via `clampVersionString`, 64 bytes, cut on a rune boundary — round 1
- [security/LOW-5] six-line block overflows a short frame because `PlaceVertical` also no-ops on overflow — detail dropped when `h < len(lines)+2` — round 1
- [security/LOW-6] multi-line ssh stderr fused into one word (`sanitizeRemoteText` drops `\n` with no separator) — `firstErrLine` applied first, reusing reconnect.go's idiom — round 1
- [rules/MEDIUM] no techdebt entry for the self-identified limitation in the PR body — `techdebt/3-3-remote-host-cannot-apply-its-own-staged-update.md` — round 1
- [qa/coverage] `promptNextUpgrade`'s moved guard was only covered by a test that passes for either reading — `TestUpgradePrompt_NoProvisionerHoldsTheAskRatherThanDroppingIt` pins "held, not dropped" and fails on master — round 1

## Deferred to techdebt (recorded, deliberately not fixed here)
- [security/LOW-4] `truncateToWidth` sums independently-measured runes and can return ~2x its budget — helper-wide (~10 call sites); `techdebt/3-3-truncatetowidth-sums-runes-not-graphemes.md`
- [ci] `TestHandleMessage_KillProcessIsWiredUpAndSingleFlighted` races the worker it observes — failed 2 of 4 CI runs on this branch, passes in isolation under `-race`, package byte-identical to master; `techdebt/3-2-flaky-kill-process-single-flight-test.md`. Not fixed here: a deterministic fix needs an `ipc.Conn` seam that does not exist yet, and building one to repair a test belongs in its own change.

## Resolved — second pass, from the code-quality and QA reports
- [code-quality/CRITICAL] the drain point did not cover the launch it targets: `promptNextUpgrade`'s "every dialog dismissal calls this again" was true of ONE dismissal (the upgrade confirm's own Esc), so a client auto-update — which opens what's-new AND drifts every host at once — filled the queue and never opened it. Drain moved to a `handleDialogKey` funnel on any return to `dialogNone`; pinned by `TestUpgradePrompt_SurvivesTheWhatsNewDialogAtLaunch` over both what's-new and the disclaimer — round 1
- [code-quality/HIGH] a host that came back ONLINE was still offered a daemon-restarting upgrade (`Offline` cleared on reconnect, `installedDests` untouched, project still non-nil) — `promptNextUpgrade` now also requires `p.Offline != nil` — round 1
- [code-quality/HIGH] `offlineRetrying` rendered "Reconnecting…" for a PARKED link with no ladder running, while `renderReconnectBanner` returns "" for an inactive link — so the only thing on screen was a false claim that contradicted `bannerCandidates`' own wording. Now an explicit `case offlineRetrying:` reading link health — round 1
- [code-quality/MEDIUM] `offlineNeedsInstall` reached a confirm written for an upgrade, warning a host with no daemon that its panes would be killed. `upgradePrompt` now carries the kind and the confirm branches on it — round 1
- [code-quality/MEDIUM] verbatim duplication: the needs-install pane printed `ErrRemoteQuilMissing`'s sentence twice, once capitalised and once behind a `dial <host>:` prefix — the detail is suppressed when it repeats the sentinel — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)

## Notes
- Three occurrences of the same private IP remain in `.claude/reviews/quil/issue-169.md`, `issue-172.md` and `tui/keybinding-registry.md`. Pre-existing on master, untouched by this branch, and left alone rather than bundled into a fix PR.
- All four agents reported. `cq-186` and `qa-186` needed three prompts each and delivered late — after the first fixes had already been pushed — so their findings landed against a moved tree and both re-verified before reporting. Every dimension is covered for round 1.
- The critical finding came from the dimension that reported LAST. Worth remembering before treating an early-returning review as complete.
