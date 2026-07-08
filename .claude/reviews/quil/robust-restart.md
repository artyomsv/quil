# Code Review State: quil / robust-restart

Last reviewed: 2026-07-08
Rounds completed: 2

## Resolved (fixed in code; do not re-raise)
- [rules/M1] Single-instance guard + 30 s crash-aware readiness wait documented in CLAUDE.md (Daemon single-instance guard / Daemon readiness wait bullets) — round 2
- [code-quality/L1] startDaemon doc comment tightened: pid==0 ⇒ daemon already listening; spawn failures os.Exit (never return 0) — round 2
- [code-quality/L2] Wait-failure after spawn now surfaces a descriptive error pointing at the daemon log (launchTUI spawnedButNotReady branch; connectToDaemon wraps with "daemon spawned but did not open socket") instead of the stale pre-spawn dial error — round 2
- [qa/gap-alive-pid] TestWaitForDaemonReadyWithin_AlivePidTimesOutNormally added — proves the crash-abort is dead-pid-specific (live pid waits full timeout, 0.15s, vs dead pid 0.00s) — round 2
- [greptile/P1-misid] Single-instance guard bare-dial misidentifies a wedged/foreign listener as a running daemon — replaced with a real MsgVersionReq/Resp handshake in a testable seam (`cmd/quild/guard.go` daemonAlreadyHealthy/Within); only a peer answering the Quil protocol defers the spawn, wedged/foreign sockets are reclaimed. Tests: RespondsToVersionReq / WedgedAcceptsButNeverAnswers / NothingListening (PR #89) — round 2
- [code-quality/M1] Pane.Type/CWD data race — spawnRestoredPane error-path writes now under PluginMu; all concurrent-reachable Type/CWD readers (workspaceStateFromSnapshot, buildPaneInfos, handlePaneStatusReq, snapshot ghost loop, handleAttach replay, spawnPane) read under PluginMu; Pane doc updated; non-vacuous -race test added (commit 9826780) — round 1
- [code-quality/L4] handleRestartPaneReq PTY=nil swap now under PluginMu (9826780) — round 1
- [code-quality/M2] Windows log-rotation hot-loop on rename failure — renameFn seam + suppressRotateUntil backoff caps retries to once per maxSize growth (b50f31f) — round 1
- [code-quality/L1] RotatingWriter.Write nil-w.f panic — errClosed guard at top of Write (b50f31f) — round 1
- [rules/M2+M3] rotate_test.go naked _ on WriteFile/ReadFile — now error-checked with t.Fatalf (b50f31f) — round 1
- [qa/gap-collision] RotatingWriter collision-suffix path — nowFn clock seam + TestRotatingWriter_CollisionSuffix (b50f31f) — round 1
- [qa/gap-rename] RotatingWriter rename-failure backoff — TestRotatingWriter_RenameFailureBacksOff (renameFn override, asserts ~22 attempts vs 200 writes) (b50f31f) — round 1
- [qa/1] Eager persistence round-trip — TestRestoreWorkspace_EagerRoundTrip via shared QUIL_HOME (newTestDaemonInDir helper); non-vacuous (9a2400a) — round 1
- [qa/2] handlePaneStatusReq deferred-pane reporting — extracted buildPaneStatus + TestPaneStatus_DeferredPaneReportsNotRunning (9a2400a) — round 1
- [rules/M6] CLAUDE.md updated: IPC dual-queue, lazy restore + spawnMu, Eager/Pending + toggle_eager + ● marker, log rotation, M5 milestone (a12c1db) — round 1
- [code-quality/L3] TestSendLoop_ExitsOnWriteError dead overflow poll → fixed settle (a12c1db) — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/H1] newSessionFn package-level mutable var — follows the established `*Fn` test-seam convention already in daemon.go (claudeSessionExistsFn, readHookSessionIDFn, opencodeHookScriptStatFn); isolating one to a struct field would be inconsistent with the documented pattern (round 1)
- [code-quality/L2] log defaults 10/3→5/10 with no migration note — docs/configuration.md already updated in the same PR; affects fresh installs only (round 1)
- [rules/M4] test naming missing TestDaemon_ type prefix — codebase convention is TestFuncName_Scenario (TestHandleUpdatePane_*, TestEnsurePaneSpawned_*); adding a type prefix would diverge from siblings (round 1)
- [rules/L6] tab_marker tests not table-driven — per-case form is explicit and readable; stylistic only (round 1)
- [rules/L7] 5 commit subjects exceed 72 chars — immutable history; noted for future commits (round 1)
- [security/L1] rotate.go Stat→Rename TOCTOU and [security/L2] int64<<20 overflow — defense-in-depth only; nil impact in the same-UID 0700-dir / trusted-config threat model (round 1)
- [security/INFO] Residual TOCTOU on the single-instance guard — two daemons that both pass the probe before either listens can still race and orphan one. Availability-only, same-UID, strictly better than the pre-guard behavior; a fully race-free fix (bind-first / pidfile flock) is a robustness follow-up, not a security fix (round 2)
- [code-quality/obs] restartDaemonForUpgrade stops the old daemon with MsgShutdown + os.Remove rather than stopDaemonEscalating, so a wedged daemon during a version upgrade can still be orphaned — pre-existing (this diff only swapped the readiness wait), narrow (wedged daemon during upgrade), out of scope for this change (round 2)
- [greptile/P1-race] Probe-race can still orphan a daemon when two spawns both fail the handshake before either listens — availability-only, same-UID, needs a millisecond-wide simultaneous-spawn window (matches the security review's dismissal of the same TOCTOU). Proper fix is a cross-platform advisory lock (flock / LockFileEx) held across probe→listen; deferred because the Windows locking path can't be tested in the Linux-only Docker build and shipping an untested lock is riskier than the narrow race (round 2)
