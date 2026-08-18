# Code Review State: global / performance

Last reviewed: 2026-08-18
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
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

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/informational] The pre-existing double-apply window between "lock released, binaries swapped" and "child running" is marginally widened by the heap release. Not introduced here, the child is gated by the version compare, and the release now runs after `cmd.Start` so the window is essentially unchanged — round 1
- [code-quality/1-note] `cmd/quil/main.go`'s update-apply call-site move has no unit test. Structural limit, not an oversight: the property is the ABSENCE of a live reference, which the compiler and GC arbitrate. The reclaimable half (`releaseHeapBeforePark`) is tested and mutation-verified against a plain `runtime.GC()` — round 1
