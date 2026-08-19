# Code Review State: global / performance

Last reviewed: 2026-08-20
Rounds completed: 3

## Resolved (fixed in code; do not re-raise)

### Round 1 (PR #167)
- [security/MEDIUM-1] Update's prologue closes a stale context menu on every message; render coalescing gated the skip on ackFocusedPane only, so an inert message could deliver a cached frame still drawing the menu (and leave MouseMode in AllMotion). Gate renamed `prologueChangedView` and the prune folded into it — round 1
- [code-quality/6] The coalescing honesty assertion was a tautology — it compared `after` against `before`, which are the same cached struct when the skip works, so it could never fail. Now compares against a forced rebuild of the returned model, and also compares MouseMode — round 1
- [security/LOW-2] `handleSubscribe` logged unconditionally per message on a path any socket-holder can loop; now logs only on a real change, malformed payload dropped to Debug — round 1
- [security/LOW-3] `Spool.Init` lacked the containment check its sibling paths carry, while being the path that now unlinks; guard added for symmetry (no escape was constructible) — round 1
- [security/LOW-4] `scrollback_lines` had no upper clamp — a stray zero OOMs the TUI on the one setting meant to reduce memory; clamped to 1,000,000 with a warn — round 1
- [qa/a] No test drove `Server.Broadcast` against an opted-out conn; added `TestBroadcast_SkipsPaneOutputForOptedOutConnOnly`, mutation-verified — round 1
- [qa/b] No test drove the `case ipc.MsgSubscribe` dispatch arm; added `TestHandleSubscribe_DispatchArmStopsPaneOutputForThatClientOnly`, mutation-verified by deleting the arm — round 1
- [qa/c] MCP bridge's opt-out send was untested; extracted as `mcpBridge.declinePaneOutput` and pinned — round 1
- [rules/1 + code-quality/5] `[ui] scrollback_lines` missing from `docs/configuration.md` (table and sample) — round 1
- [rules/2] `daemon-lifecycle.md` documented `Broadcast` as fanning to every conn; `wantsFrame` now precedes `enqueue`. Section added for `MsgSubscribe` — round 1
- [rules/3] `pane.go` justified its package var by citing `SetRemoteDest` (does not exist) and `SetRecentCWDs` (a method on `*Model`); now cites `version.SetUpdatesEnabled`, the real precedent — round 1
- [code-quality/2] Two doc comments orphaned by inserting a function above their target (`Broadcast`, `respawnSelf`) — round 1
- [code-quality/3] `Spool.Init`'s own doc still described truncation — round 1
- [code-quality/4] Four stale "100 ms spinner tick" comments; now name `workSpinnerInterval` rather than restating a number — round 1
- [code-quality/8] `releaseHeapBeforePark` ran before `cmd.Start`, paying a STW GC on the user-visible relaunch path; moved between Start and Wait — round 1
- [code-quality/9] The `finalModel = nil` comment overstated its effect (Go liveness is precise); softened, and now names `p` — pinned by the toast listener — as the reference that genuinely outlives the respawn — round 1
- [code-quality/10] Render coalescing silently changed `view(n=...)` to mean rebuilds rather than frames; added `skipped=` so the stat this branch's analysis rests on stays honest — round 1
- [code-quality/11] `removeFn` stubbing test must stay serial; documented at the var — round 1
- [code-quality/12] Test panes were never disposed (parked drain goroutine + scrollback each); `coalesceModel` now takes `*testing.T` and cleans up — round 1
- [code-quality/13] Scrollback assertion passed on a scrollback of ZERO — the exact failure the clamp prevents; now asserts a band — round 1
- [hooks-and-sessions.md] Rule text said "Spool init truncates stale files"; corrected, with the measurement that motivated the change — round 1

### Round 2 (PR #175)
- [code-quality/HIGH + qa/HIGH] **A shipped bug**: the overlay branch of `handlePaneOutput` read `overlayChanged := false` instead of calling `paneIsVisible`, so a visible overlay pane (lazygit, hunk, k9s, lazysql) had its live output coalesced away and froze on screen once boot settled. A stray line left by a local mutation-testing restore that did not take, captured by a later commit. Fixed in `7e41192`; the whole delta from the introducing commit was audited and this was the only stray — round 2
- [code-quality/HIGH + qa/HIGH] `TestUpdate_VisiblePaneOutputAlwaysRebuilds` sent one UNPRIMED chunk, so the once-per-pane `liveOutputSeen` branch forced a rebuild regardless of visibility and the assertion never reached the gate. The whole package stayed green with visibility hardcoded `false` at BOTH call sites. Now primes via `primePane`; overlay half covered by `TestUpdate_ActiveTabOverlayOutputAlwaysRebuilds` — round 2
- [code-quality/related] Two of the three "deliberate conservatism" branches were pinned by nothing (deleting both flags left the suite green). Every conservative branch is now individually mutation-pinned: both ghost transitions, the restore settle, the first live frame, the CWD change — round 2
- [qa/ghost] The ghost-flip branch had no `changedView` treatment and no comment explaining the omission. Flags added at the two TRANSITION guards — not the assignments, which run on every chunk and would coalesce nothing — plus `TestUpdate_HiddenPaneGhostToLiveTransitionRebuilds` — round 2
- [qa/LOW] `spool.go` rotation boundary: the threshold test seeded `rotationThreshold+1`, so `<` mutated to `<=` undetected — a file sitting at exactly 16 MiB would take the skip and never rotate until it grew a byte. Added `TestSpool_Tick_RotatesAFileAtExactlyTheThreshold` — round 2
- [rules/MEDIUM] The new `tui-rendering.md` coalescing section claimed "nothing outside the pane's own top border draws a CWD". `renderStatusBar` does, for `tab.ActivePaneModel()` of the active tab. The gate is unaffected (that pane is visible by definition) but the absolute claim was false; corrected in the rule, the code comment, and the plan — round 2
- [security/informational] Windows NTFS updates a directory entry's size lazily while a file has an open write handle, so a producer mid-write could present a stale size and take the spool skip. Bounded to one 200 ms tick for today's short-lived producers; recorded in the code and the rule as the condition under which the shortcut stops being safe — round 2
- [CI] Pre-existing `internal/daemon` race surfaced by this branch's timing: four `pane.Type` writes landing after `CreatePane` publishes the pane, read under `PluginMu` by `workspaceStateFromSnapshot`. All four guarded; `replacePaneAt` deliberately left alone because `NewPane` returns an unpublished pane. Regression test verified to discriminate — round 2

### Round 3 (PR #175)
- [code-quality/HIGH] `TestPaneType_WriteDoesNotRaceSnapshotRead` hand-rolled its own `PluginMu` pair instead of driving the production write, so it asserted only that a guarded write does not race a guarded read — true unconditionally. Proven by checking `daemon.go` out at the pre-fix commit and watching the suite stay green. Both writes extracted to `setPaneType`/`setPaneCWD`; the tests now drive those, and deleting either lock fails the matching test — round 3
- [security/HIGH + qa/HIGH] `handleUpdatePane` wrote `pane.CWD` without `PluginMu` while `workspaceStateFromSnapshot`, `buildPaneInfos` and `buildPaneStatus` all read it under the lock, and `daemon-lifecycle.md` states the contract outright. Window far wider than the Type race — every `cd` in every pane, via OSC 7 → `MsgUpdatePane` on a conn dispatch goroutine. Torn string is persisted, broadcast, and passed to `os.Stat` on restore. Fixed via `setPaneCWD`, pinned by `TestPaneCWD_WriteDoesNotRaceSnapshotRead` — round 3
- [code-quality/doc] `view_coalesce_test.go` carried a second copy of the false "nothing outside the pane draws a CWD" claim that round 2 fixed only in `model.go`; corrected — round 3
- [code-quality/doc] Two comments still said "three branches set changedView" where there are now five (both ghost transitions, restore settle, first live frame, CWD); corrected in `model.go` and `view_coalesce_test.go` — round 3

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/informational] The pre-existing double-apply window between "lock released, binaries swapped" and "child running" is marginally widened by the heap release. Not introduced here, the child is gated by the version compare, and the release now runs after `cmd.Start` so the window is essentially unchanged — round 1
- [code-quality/1-note] `cmd/quil/main.go`'s update-apply call-site move has no unit test. Structural limit, not an oversight: the property is the ABSENCE of a live reference, which the compiler and GC arbitrate. The reclaimable half (`releaseHeapBeforePark`) is tested and mutation-verified against a plain `runtime.GC()` — round 1
- [security/informational] `openFile` is a new mutable package var. Unexported, written only by tests, identical in shape to the existing `removeFn` precedent in the same file; no in-process attack surface. The only real hazard is the `t.Parallel()` data race, documented at the declaration — round 2
- [code-quality/cosmetic] Consecutive blank lines in `view_coalesce_test.go` reported as a gofmt violation. Verified false: gofmt does not collapse blank lines inside function bodies and reports nothing on any file this branch touches. Tidied anyway, but the finding as stated was wrong — round 2

- [rules/MEDIUM] `d31343e` is `docs:`-typed but its diff carried the overlay regression (a stray mutation line, fixed two commits later in the same PR). Accurate finding, but the only available remedy is rewriting a pushed branch: the repo squash-merges so the intermediate commit never reaches master, and force-push is prohibited here because it reds the `changes` CI job. Dismissed as unfixable-without-a-worse-cost, recorded so the history is explicable to a future bisect — round 3
- [qa/scope] `pane.Name` is written unguarded in the same handler, but is also READ unguarded at six sites, so guarding only the write establishes no happens-before. Fixing it means touching every reader — a separate change with no documented contract and no CI signal behind it. Deliberately out of scope for this PR — round 3
