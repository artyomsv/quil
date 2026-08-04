# Code Review State: quil / input-ordering

Last reviewed: 2026-08-04
Rounds completed: 2

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
- [qa/4] overlay path covered by existing TestHandleOverlayKey_PlainRune_ForwardsToOverlay — round 1
- [qa/5] empty-data case test (TestForwardInputBytes_EmptyData_NoEnqueue) — round 1
- [security] CLEAN — round 2 re-review of the multi-daemon surface: dest resolution correct at all four producers (all target the active pane, so destOfPane's activeDest() fallback agrees with a successful lookup), overlay panes covered by destOfPane's explicit overlayPane check, destLocal sentinel never reaches the wire, no payload bytes in any log or error path, all producers allocate fresh slices, Semgrep p/owasp-top-ten + p/golang clean — round 2
- [code-quality] CLEAN — round 2: verified (not assumed) that launchTUI always builds a *Router and finishReconnect mutates it in place, so the forwarder's Init-time copy of m.client stays valid; blocked-Update-on-full-queue confirmed unreachable (ipc.Conn.Send is select/default on every path); sendInputToPane's len/paneID guard preserved verbatim inside enqueueInput — round 2
- [rules/H1] CHANGELOG.md entry added under [Unreleased]. The CI `changelog` job (.github/workflows/ci.yml) hard-fails any PR touching non-test cmd//internal/ Go without it — this would have blocked the merge — round 2
- [rules/M2] TUI-side pane-input-pipeline invariant documented in .claude/CLAUDE.md (mirroring the existing daemon-side bullet) and the wheel-forwarding paragraph in .claude/rules/tui-rendering.md updated to note it now rides the shared queue — round 2
- [qa/6] pasteClipboard queue-sharing covered (TestPasteClipboard_ReadsOnCmdButEnqueuesOnUpdate) — the fourth producer previously had no ordering test — round 2
- [qa/7] forwardOne panic-recovery branch covered (TestForwardOne_PanicKeepsDrainerAlive) — the round-1 M1 hardening was previously unexercised — round 2
- [greptile/P1-a] Paste no longer bypasses event ordering. pasteClipboard's tea.Cmd did the enqueue itself, making paste a second producer that could be overtaken by a key typed during the clipboard read, and reading pane/dest state off the Update goroutine. The Cmd now performs the clipboard READ only and returns clipboardPastedMsg; Update does the enqueue. enqueueInput is now single-producer — round 2
- [greptile/P1-b] Shutdown no longer abandons queued input. inputForwarder's select could take the inputDone branch while entries were still buffered (Go picks pseudo-randomly between two ready cases), silently dropping input the Update goroutine had already accepted — the exact loss the blocking enqueue exists to prevent. It now drains inputCh before returning; safe because StopInputForwarder runs after tea.Program.Run returns. Mutation test confirmed the pre-fix behaviour lost 8 of 8 buffered entries — round 2

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)

## Notes
- Every round-2 fix is mutation-verified: reverting the implementation makes the corresponding test fail, and restoring it makes it pass. This includes the two greptile P1s.
- Not runtime-verified: the TUI input path is Update-side and cannot be driven headlessly (see `.claude/skills/verify`). A human smoke test under CPU load remains the final confirmation.
- Pre-existing and unrelated: `TestOfferRemoteInstall_ClaimsTheHostLacksQuilOnlyWhenProbed` (cmd/quil) fails under `-count=5` on a PRISTINE origin/master checkout too. Not caused by this work; not fixed here.
