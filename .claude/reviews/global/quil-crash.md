# Code Review State: global / quil-crash

Last reviewed: 2026-08-09
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/C1] SendBlocking did not increment Conn.pending, making Client.Flush a permanent no-op and closeClient discard queued frames — round 1
- [code-quality/I2] Data race on package-level clientSendTimeout between two t.Parallel tests (confirmed by CI race detector; local test-race did not reproduce) — round 1
- [code-quality/I3] Deferred (Pending) panes never converge — handleResizePane skips a nil PTY without recording the size, so the resize diff re-sent 32 frames per broadcast on a 33-tab lazy-restore workspace — round 1
- [code-quality/S4] Dead `cols > 0` term in diffResizes (paneVTSize floors both dimensions at 1) — round 1
- [code-quality/S5] sizedOnce doc named resetForReattach; the clearing is in armReattachReset — round 1
- [code-quality/S7] sizedOnce had no disposal path; now pruned in applyWorkspaceState's surviving sweep — round 1
- [code-quality/S8] runBatch duplicated the package's existing runCmd helper — round 1
- [code-quality/S9] `body` built but only Payload searched; renamed to `detail` — round 1
- [code-quality/P7a] ReportedCrashConfiguration test fixture modelled a fully-spawned workspace; added LazyRestoreAtScale companion — round 1
- [code-quality/P7b] client_timeout_test's closing assertion re-read an overflow flag the same call path had just set; now waits for close and checks Receive fails — round 1
- [code-quality/P5] No signal when a broadcast's dest matches no project (silent total suppression); log line added — round 1
- [rules/1] gofmt: sizedOnce insertion split model.go's alignment group — round 1
- [rules/2] .claude/CLAUDE.md layout-persistence invariant described the removed unconditional sweep — round 1
- [rules/3] daemon-lifecycle.md described SendBlocking as bulk-replay-only and drew no Conn.Send vs Client.Send distinction — round 1
- [rules/4] Five comments asserted "client.Send is non-blocking" as a load-bearing safety fact; unbounded wait could block the Update goroutine. Bounded via clientSendTimeout and all comments re-derived — round 1
- [rules/5] layoutAgrees had no direct unit test; added table-driven TestLayoutAgrees incl. the malformed-stored branch — round 1
- [qa/1] peerLabel had zero coverage across its four branches — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/S6] sizedOnce marked before the send is attempted — a frame dropped for an unreachable dest costs that pane its first-resize kick until the next reattach. Accepted: the daemon fires its own resizeKick on first PTY output, and the sends are fire-and-forget tea.Cmds with nothing to report back. Documented at the call site (round 1)
- [code-quality/S10] Commit message says Send returns ErrSendOverflow "as before" — true only for the first expiry; later calls return ErrConnClosed. Zero consumers of either outside internal/ipc (round 1)
- [code-quality/S11] sendHeadroom halves the client's effective queue — a reservation with no meaning on a client. Not a bug; a burst parks at 32 rather than 64 (round 1)
- [qa/2] TestWorkspaceState_FirstResizeAfterAttach_IsAlwaysSent passes against pre-fix code too. Kept deliberately: it pins the new suppression logic's edge case (fails if diffResizes ever over-suppresses), it is simply not a regression guard on its own (round 1)
- [security] No report delivered — agent went idle without findings. Not treated as a clean bill of health (round 1)

## Notes
- Security agent never reported. The four probes it was given (blocking Send DoS/deadlock, peerLabel log injection, hostile daemon suppressing sends via cols/rows, sizedOnce growth) remain formally unanswered, though 2, 3 and 4 were covered incidentally by the code-quality pass.
- Deferred to its own change: throttling broadcastState(). ~28 immediate call sites; the safe form needs a synchronous flush before respondTo, which is a package-level function called 39 times in daemon.go with no daemon receiver, and the worktree-create path depends on broadcast-before-response ordering.
