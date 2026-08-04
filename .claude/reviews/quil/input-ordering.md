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

- [greptile/P1-a2] Paste no longer targets the wrong pane. pasteClipboard resolved the destination when the clipboard read FINISHED, so switching panes during the read (the image path decodes a DIB, encodes a PNG and writes it to disk — easily long enough) delivered clipboard contents into whichever pane had become active. The target pane is now bound on the Update goroutine at request time and carried on clipboardPastedMsg; sendClipboardToPaneID looks it up across every project and drops the paste if the pane closed meanwhile. Mutation test confirmed the pre-fix path delivered to the wrong pane — round 2

- [greptile/P1-c] Shutdown drain is now AWAITED, not merely signalled. The round-2 drain fix stopped the loss at the channel but not at the socket: StopInputForwarder only closed inputDone, and launchTUI closed the IPC client immediately after, so a connection closed mid-drain discarded whatever had not been written — the same lost keystrokes one layer down. inputForwarder now closes inputIdle when it finishes draining and StopInputForwarder blocks on it (bounded by inputDrainTimeout=2s, since client.Send is non-blocking on every path and a stall should degrade to "exit anyway, having logged it" rather than a TUI that will not quit). Mutation test confirmed the un-awaited path delivered 0 of 5 entries — round 2

- [greptile/P1-d] The drain now reaches the SOCKET, not just the channel. Awaiting inputCh was still not enough: ipc.Conn.Send is non-blocking (it hands the frame to sendLoop), and Conn.Close closes done, which makes sendLoop return without writing what is left — so the exit path discarded frames the caller had been told were accepted. Conn now counts must-deliver frames in flight (pending) and exposes a bounded Flush; Model.closeClient flushes before releasing each connection, so flush-then-close is structurally paired and no caller can close without flushing. Droppable output frames are deliberately not counted (a busy pane would hold the count above zero); a closed conn returns immediately rather than burning the timeout. Tests: TestConn_FlushWaitsForQueuedFrames, TestConn_FlushOnClosedConnReturnsImmediately — round 2

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [greptile/P1-a1] "A key typed during the clipboard read reaches the queue before the paste." Accurate, and deliberately left as-is. The paste necessarily enters the queue when the READ completes; closing the gap means reserving its slot at request time, which makes a slow clipboard read head-of-line block every keystroke behind it — trading a rare self-inflicted interleave for a visible freeze of the entire input stream on the image path. The wrong-pane half of the same finding WAS fixed (see greptile/P1-a2), because misdelivery is silent whereas a mis-ordered paste is immediately visible. Rationale recorded in the clipboardPastedMsg doc comment (round 2)

## Notes
- Every round-2 fix is mutation-verified: reverting the implementation makes the corresponding test fail, and restoring it makes it pass. This includes the two greptile P1s.
- Not runtime-verified: the TUI input path is Update-side and cannot be driven headlessly (see `.claude/skills/verify`). A human smoke test under CPU load remains the final confirmation.
- Pre-existing and unrelated: `TestOfferRemoteInstall_ClaimsTheHostLacksQuilOnlyWhenProbed` (cmd/quil) fails under `-count=5` on a PRISTINE origin/master checkout too. Not caused by this work; not fixed here.
