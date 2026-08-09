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

## Round 1 addendum — security agent (report arrived late, after the first three were closed out)

Verdict: approve, 1 MEDIUM, 6 LOW, no blockers. Two of its findings had already
been fixed independently (the `t.Parallel` race; the growth half of the MEDIUM).

- [security/M1a] sizedOnce unbounded growth — already fixed by the disposal-sweep prune
- [security/M1b] sizedOnce keyed by pane id alone while every sibling structure is dest-scoped; one dest could consume another's first-resize kick — now keyed by sizedKey(dest, paneID)
- [security/L1] Post-overflow sends returned ErrConnClosed while the timing-out caller got ErrSendOverflow, contradicting ErrSendOverflow's documented contract — normalised
- [security/L2] Client give-up path set the overflow flag and closed the conn with no log line, i.e. the client-side overflow (the 2026-08-09 shape) took the silent path — now logs peer + depth
- [security/L4] peerLabel unbounded and unsanitized into a log the F1 viewer renders — capped at 120 and C0/DEL replaced
- [security/L5] Implementation-dependent uint16 conversion on daemon-supplied cols/rows — range-checked
- [security/L6] clientSendTimeout package-var race under t.Parallel — already fixed

Dismissed:
- [security/L3] Residual ~5 s Update stall via enqueueInput against a repeatedly-wedging remote daemon. Deliberate and documented; strictly better than the pre-branch behaviour (which ended the session). Recorded so the ceiling is on record.

Filed as tech debt rather than fixed here:
- Conn.write's 30 s deadline does nothing over ssh (stdioConn.SetWriteDeadline returns ErrNoDeadline; the error is discarded) and sendLoop returns without closing on write failure. No longer reachable from the client send path, since clientSendTimeout fires on its own timer. See techdebt/3-2-conn-write-deadline-absent-over-ssh.md
