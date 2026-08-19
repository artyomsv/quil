# Code Review State: quil / issue-169

Last reviewed: 2026-08-19
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/M1 + code-quality/MEDIUM] Throttle measured enqueue time, not delivery — a child that stopped reading stdin held the kick and received both back to back on resume (measured 17.9 µs apart in review). Fixed by drain counters (`inputEnqueued`/`inputWritten`), dropping a held kick while the earlier one is queued, and restamping `lastRedrawAt` on write completion so the cooldown runs from the child's receipt — round 1
- [security/L1] `deferredRedrawKey` staleness test missed teardown — `releasePanes` nils neither `PTY` nor `ExitCode`, so a kick firing during teardown saw a healthy pane. Added `Pane.inputStopped`, set in `StopInput` — round 1
- [security/L2 + code-quality/LOW-3] A fresh child inherited the previous one's `lastRedrawAt` and was held blank up to a cooldown. `spawnPane` now clears `lastRedrawAt`/`redrawSeq` in the span that already zeroes `appliedCols/appliedRows`; `handleRestartPaneReq` reinstalls through the same function — round 1
- [code-quality/LOW-1] `log.Printf` ran while `PluginMu` was held, against the rule this file's own comments cite. Moved below the unlock — round 1
- [code-quality/LOW-2] Interface identity comparison (`pane.PTY != pty`) would panic on any future value-receiver `apty.Session`, inside a `time.AfterFunc` goroutine. Replaced with a `Pane.ptyGen` counter — round 1
- [code-quality/LOW-4] Three tests established their decisive condition after an unbounded `waitForInput`, turning a lost race into a failure that read like a throttle bug. Added `requireHeldKick` setup guards — round 1
- [code-quality/LOW-5] A refused enqueue (full queue) was silent while the stamp was taken anyway. Now logged; the stamp is deliberately kept and the reason documented — round 1
- [qa/coverage] `StopInput`'s timer cancel — the correctness half of the destroy path — had no test. Added `TestStopInput_CancelsAHeldRedrawKick`, asserting the timer rather than the bytes (after teardown the writer is gone, so "nothing reached the child" holds either way and would have survived deleting the cancel) — round 1
- [rules/LOW] Two techdebt files used `Complexity: Moderate`, outside the template vocabulary. Changed to `Medium` — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/L3 + code-quality note] A single kick still arms claude-code's `/clear` hint, so a user's own `Ctrl+L` within two seconds still clears — and the cooldown arguably widens that window. Tracked as issue #171 rather than fixed here: closing it means replacing the byte with a repaint trigger that has no keyboard semantics, which needs measurement against a real `claude` in both renderers (round 1)
- [code-quality note] On a restore-attach the resize kick is the held one, so a pane can show content wrapped at the old width for up to 3 s — the overlapping-banner symptom, now transient rather than permanent. Deliberate: a stale frame for 3 s beats a wiped conversation. The alternative (delay the ATTACH kick ~250 ms so the client's first resize coalesces into it) is recorded for later (round 1)
- [security, pre-existing] `artyom@192.168.6.12` remains recoverable from this public repo's git HISTORY (~8 commits). Unrelated to this PR, needs `git filter-repo`, already surfaced to the user separately (round 1)
