# Code Review State: quil / remote-phase-2-reconnect

Last reviewed: 2026-07-30
Rounds completed: 1

Scope: PR #113, `git diff master..HEAD` on `feat/remote-phase-2-reconnect`.
Remote daemon Phase 2 (RD-010…RD-016). Four agents (security-officer,
code-reviewer, rules-compliance, qa) plus Greptile, which scored 5/5 on both
passes but raised a **P1 inline comment on the second** — see the greptile/P1
entry below. Worth noting for next round: the score is not the finding list, and
reading only the score missed it the first time.

## Resolved (fixed in code; do not re-raise)

- [code-quality/CRITICAL-1] `resetPanesForReattach` wiped panes the daemon never repaints — `handleAttach` replays only `ghost_buffer = true` plugins, so opencode/lazygit/k9s/lazysql were cleared with nothing coming back. The first fix gated on the plugin registry and was itself wrong (see greptile/P1); the reset is now armed and consumed by the daemon's actual replay — round 1
- [code-quality/2] `verifyRemoteLink`'s read deadline did not bound the LOOP — held-remainder and bufio fast paths never block, so continuous broadcast traffic looped forever. Added an explicit wall-clock bound; a failed `SetReadDeadline` is now logged rather than fatal — round 1
- [security/L1 + code-quality] `LinkErr` was read AFTER `Close` on the verify-failure path, contradicting the invariant `version_gate.go` documents and pins. Read before Close now; the dial-error branch's fallback removed as unreachable (`link` is always nil there) — round 1
- [security/L3 + code-quality/11] Version probe accepted any `MsgVersionResp` regardless of ID, and swallowed a decode error. Now matches `probeRequestID`, logs a mismatch as a warning, and reports an undecodable payload — round 1
- [code-quality/4] `promptUpgradeClient` printed 12 lines after `client.Close()` without `restoreConsoleMode()` — reachable in remote mode when the remote daemon is newer — round 1
- [code-quality/12] Backoff reset to zero on every success, so a crash-looping remote got ~2 ssh spawns/sec forever with the counter stuck at 1. Added `reconnectFlapWindow` carry-forward — round 1
- [security/M2 partial] Unbounded retries at a flat 30 s cap produced ~120 failed authentications an hour, which a default fail2ban jail bans — reachable without an attacker, because a batch redial cannot use a passphrase-protected key the startup dial prompted for. Mitigated by rate decay to ~33/hour with steady-state spacing under the threshold. Full classification deferred — see Deferred below — round 1
- [security/L2 + code-quality/5] A discarded late reconnect leaked its client, ssh child and remote `quil --stdio`. Added `SetClientCloser`; `redialTickMsg` now checks the `attempt` it carries, making a second concurrent dial impossible by construction — round 1
- [code-quality/6] The reconnected client was never closed on exit — `defer client.Close()` captured the startup client. `Model.CloseClient()` called beside `FlushNotes()` — round 1
- [code-quality/7] `redialResultMsg` lacked the `active` guard its sibling had, so a late failure could leave a session with no timer and no banner — round 1
- [security/L5] Tabs survived into the one-row banner; the transport preserves `\t` and `lipgloss.Width` measures it as 0 cells, so a few hundred passed the width budget then consumed screen rows. `firstErrLine` maps them to spaces — round 1
- [security/L6] An empty `keybindings.quit` left a frozen session with no exit, since `kbMatches` returns false for an empty binding. Added a hardcoded `ctrl+q`/`ctrl+c` escape — round 1
- [code-quality/8] `reconnectDelay`'s overflow comment said the shift count reaches 64; it is 56 at attempt 57 — round 1
- [code-quality/9] `bannerCandidates` indexed without a bounds guard on the render path; the "every rung keeps ctrl+q" invariant was convention-only. Guard added, invariant pinned — round 1
- [code-quality/10] Context menu and command palette were stranded open and frozen on link loss, with Esc swallowed. Both closed in `beginReconnect` — round 1
- [qa/TAUTOLOGICAL] `TestReconnect_NewListenLoopCarriesTheNewGeneration` compared a field to itself and could not fail — proven by building the listen cmd before the generation bump and watching the suite pass. Now runs the cmd `finishReconnect` returned and asserts a literal — round 1
- [qa/14 + rules/1] `TestRestoreConsoleMode_SkipsHandlesThatWereNotConsoles` asserted nothing; `TestReconnect_InputResumesWhenNotReconnecting` discarded both returns; `fakeDaemon` dropped a write error. Syscall made injectable and call count asserted; observable control arm added; error checked — round 1
- [qa/2] No coverage of the freeze or the banner with a dialog open. Both added — round 1
- [rules/2] `.claude/CLAUDE.md` had no entry for the Phase 2 subsystem, and its ResetVT-skip line needed scoping (the claim was incomplete, not false — the ghost→live path is untouched) — round 1
- [security/M3] A real private LAN address and username in `internal/config/remote_test.go` in a public repo. Replaced with RFC 5737 documentation space, in its own commit since it predates this branch — round 1

- [greptile/P1] The first fix for the critical finding predicted the daemon's replay from the Model's plugin registry — loaded from THIS machine's `config.PluginsDir()` while `handleAttach` decides from the DAEMON's. Different machines in remote mode; even locally the TUI reloads ahead of the daemon. A mismatch blanks a pane or doubles it. Replaced with an armed flag consumed by the daemon's first replayed chunk — no registry, no prediction. The overlay branch of `handlePaneOutput` consumes it too, since it returns early — round 1

## Dismissed (acknowledged, will not fix)

- [security/semgrep] `math/rand` for backoff jitter — the jitter spreads retry timing and guards nothing; `reconnectDelay` takes it as a parameter precisely so the curve is deterministic under test. `crypto/rand` adds an error path for no security benefit (round 1)
- [code-quality/13] `type Client = tuiClient` alias renders oddly in `go doc`. Cosmetic; renaming touches unrelated call sites for no behavioural gain (round 1)

## Deferred to techdebt (tracked, not dismissed)

- [security/M1 + code-quality/3] Batch ssh dials buffer stderr with no cap and no logging, and Phase 2 made those conns session-length → `techdebt/3-3-batch-ssh-stderr-unbounded-and-unlogged.md`
- [security/M2 remainder] Reconnect cannot classify a permanent ssh failure; exit 255 covers both permanent and transient, and discriminating needs ssh's prose, which the project avoids by policy → `techdebt/3-3-reconnect-cannot-classify-permanent-ssh-failure.md`
- [qa/1] `redialRemote` has no unit coverage as a whole — `internal/transport`'s `startCommand` seam is unexported and not surfaced to `cmd/quil`. Would need a new seam; the pieces it composes are individually covered

## Notes for the next round

Phase 2 is code-complete but **not fully verified on a real link**. The two live
checks that passed both used a *terminal* pane, which code review showed is
exactly the case that works — reconnecting an **opencode** pane is the highest
value outstanding check. See `docs/roadmap/remote-daemon.md` § Phase 2 for the
status table.
