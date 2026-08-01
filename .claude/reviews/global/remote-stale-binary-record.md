# Code Review State: global / remote-stale-binary-record

Last reviewed: 2026-08-01
Rounds completed: 1

Scope: `origin/master..HEAD` on `fix/remote-stale-binary-record` (PR #118) —
RD-045/RD-046, replacing the config-based remote install loop guard with a
live host probe.

## Resolved (fixed in code; do not re-raise)
- [security/M-1] `reportRemoteBinaryWontRun` interpolated the remote-reported path raw inside single quotes in a command the message tells the user to paste — now `ShellSingleQuote`d — round 1
- [security/M-2] The record-correction arm adopted a probe-reported path without consent and without `ExistingDirWritable`, bypassing `PlanTarget`'s group/other-writable guard — now gated on the same `rw` — round 1
- [security/L-3] `isControl` covered C0/DEL/C1 only, so printable bidi overrides survived path validation and could reverse a path shown above the `[y/N]` prompt — U+202A-202E and U+2066-2069 now rejected — round 1
- [code-quality/C-1] Nil client dereference on the retry pass: `gateVersionCheck` returns nil when it wants another re-dial, `remoteInstallRetry` is already consumed, and `defer client.Close()` has no nil guard — now reports and exits — round 1
- [code-quality/I-2] `recordRemoteBinary`'s doc comment left attached to `mutateConfig` by the refactor — round 1
- [code-quality/I-3] "Every line here is printed BEFORE the probe runs" was false after the reorder, and `RemedyReinstall` with an empty `ExistingPath` (execute bit removed → 126 from the shell, none from the probe) announced a binary the probe had just said was absent — now prefers the probe — round 1
- [code-quality/I-4] Three comments plus a log line still said a retry means an install succeeded; correcting a stale record resolves a launch without installing — round 1
- [code-quality/I-5] Up to a full timeout of terminal silence: the `Checking <dest>…` notice lived in the branch probe-reuse skips, while ssh may be waiting for a passphrase behind it — round 1
- [code-quality/I-6] The install path called `recordRemoteBinary` directly, bypassing the new seam, so a stubbed test could write the developer's real config — round 1
- [code-quality/I-7] `resetRemoteSetupState` left `recordRemoteBinaryFn`/`clearRemoteBinaryFn` pointing at the live implementations — now fail loudly — round 1
- [code-quality/S-10] The `setupOptions.probe` comment claimed a zero `Probe` is "a real answer"; `RunProbe` cannot produce one (`ParseProbe` rejects an empty HOME). The real hazard is `PlanTarget` yielding a relative `.local/bin` — round 1
- [code-quality/S-11] Record-write failure now names the remedy (`quil remote setup`, or the literal config stanza) instead of a bare error — round 1
- [code-quality/GP] Loop termination depends on `remote-probe.sh`'s search order, which no Go test can protect — now documented at the guard — round 1
- [qa/G-1] Nothing asserted the "one ssh round trip, not two" claim — added `TestRunRemoteSetup_ReusesASuppliedProbe` and `TestOfferRemoteInstall_ProbesExactlyOnce` — round 1
- [qa/G-2] No disk-level test for `mutateConfig`/`recordRemoteBinary`/`clearRemoteBinary`, including the `fs.ErrNotExist` tolerance the comment calls load-bearing — added `TestRecordAndClearRemoteBinary_RoundTripOnDisk` — round 1
- [rules/DOC] `.claude/rules/remote-transport.md` verified to describe the new invariant rather than the replaced one — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [security/L-4] `mutateConfig`'s load → mutate → save is last-writer-wins against a concurrent `config.Save` from a TUI exiting with `configChanged`. Pre-existing on `recordRemoteBinary`, not introduced here; a cross-process advisory lock is a larger change than this PR's scope and is availability-only, same UID (round 1)
- [rules/LOW-1] Commit *body* lines exceed 72 columns in a few places. Subjects comply (65 and 70). Fixing means force-pushing rewritten bodies over an open PR for no reader benefit (round 1)
- [rules/LOW-2] The four `*Fn` package-level seams are globals rather than constructor injection. Flagged informational by the rules agent itself: it mirrors the established pattern in this exact file (`isReleaseFn`, `offerRemoteInstallFn`), and diverging would be the inconsistency (round 1)
- [code-quality/S-9] Replace the `(probe, done, retry)` tri-value return with a named `healOutcome` type. Fair readability point, but churn across five call sites on a branch already under review; revisit if the truth table grows a fourth state (round 1)
- [code-quality/S-12] Consolidate the four seams into one small store interface. Same reasoning as LOW-2; would also subsume I-6, worth doing if this area is refactored again (round 1)
- [code-quality/S-13] `healRemoteRecord` prints "Reconnecting…" and `main.go` then prints "Attaching…". Cosmetic double-message on a rare path (round 1)
- [qa/G-3] `TestOfferRemoteInstall_UpgradeRemedy_SkipsTheProbe` proves `probeRemoteFn` is not called but not that `runRemoteSetup`'s own `RunProbe` fallback is avoided. The QA agent noted the dev-build short-circuit makes this unreachable in practice (round 1)
