# Code Review State: quil / input-ordering

Last reviewed: 2026-06-20
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security] CLEAN — no findings (no keystroke/secret logging, single bounded goroutine, no slice aliasing) — round 1
- [code-quality/M1] inputForwarder hardened against a dead drainer: per-entry recover (forwardOne) keeps the goroutine immortal so the blocking enqueue can never deadlock the Update loop; rationale documented — round 1
- [code-quality/L1] forwardInputBytes guards len(data)==0 — skips useless zero-length frame — round 1
- [code-quality/L2] inputForwarder doc comment leads with lifecycle + cross-references idleChecker/hookEventsWatcher convention — round 1
- [rules/M1] explicit goroutine shutdown path added — inputDone channel + select in inputForwarder + StopInputForwarder, wired from main.go TUI-exit path — round 1
- [rules/M2] inputOrderTestModel takes *testing.T and calls t.Helper() — round 1
- [qa/1] end-to-end inputForwarder ordering test added (TestInputForwarder_DrainsToClientInTypedOrder; small buffer forces mid-flight drain) — round 1
- [qa/2] handleKey default-branch coverage (TestHandleKey_PlainRunes_EnqueueInTypedOrder) — round 1
- [qa/3] nil-tab / nil-active-pane early-return tests (TestForwardInputBytes_NilTabOrPane_NoEnqueue) — round 1
- [qa/5] empty-data case test (TestForwardInputBytes_EmptyData_NoEnqueue) — round 1
- [qa/4] overlay path covered by existing TestHandleOverlayKey_PlainRune_ForwardsToOverlay — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)
