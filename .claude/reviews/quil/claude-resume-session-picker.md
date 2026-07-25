# Code Review State: quil / claude-resume-session-picker

Last reviewed: 2026-07-26
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/M1 + code-quality/8] `claude_sessions_req` had no single-flight; added `Daemon.sessionScanning` atomic mirroring `updateStaging`, rejection echoes the CWD — round 1
- [security/M2 + code-quality/7] "already open in another pane" was enforced only in the TUI; `applyResumeSessionID` is now a Daemon method that consults `liveClaudeSessionIDs()` and drops the id on conflict, closing both the bypass and the TOCTOU — round 1
- [security/M3] `pane.PluginState` read without `PluginMu` in `resolveSpawnArgs`; the resume id is now a parameter captured under the lock by `spawnPane` — round 1
- [security/L1] `resumeSessionIDRe` admitted all-dash / leading-dash argv tokens; tightened to the canonical UUID shape, protocol.go comment corrected, test table extended with the gap cases — round 1
- [security/L2] `sanitizeTitle` missed Unicode `Cf` (bidi override / isolates / ZWSP); now dropped alongside control chars — round 1
- [security/L3] `*.jsonl` symlinks passed the `!IsDir()` filter and were followed by `os.Open`; now requires `info.Mode().IsRegular()` — round 1
- [security/L4 + code-quality/3] `resume_session_id` was never cleared and was re-applied by Alt+R (which spawns with `restoring=false`), resurrecting a session the user left via `/clear`; `spawnPane` now prefers a hook-recorded id and retires the key, and `refreshPluginStateFromHooks` deletes it once the hook confirms a session — round 1
- [security/L5] TUI rendered daemon-supplied titles unsanitized; `applyClaudeSessions` re-applies `claudesessions.SanitizeTitle` once per response — round 1
- [security/L6 + code-quality/suggestion] `liveClaudeSessionIDs` counted exited panes, blocking their session forever; now gated on a live PTY — round 1
- [code-quality/1] Session listing scanned the raw CWD while the pane spawns in the symlink-resolved one; `claudeSessionsResponse` now scans `EvalSymlinks(req.CWD)` while still echoing verbatim — round 1
- [code-quality/2] A timed-out or failed scan could never be retried; `ensureSessionScan` now treats only `scanning`/`ready` as settled — round 1
- [code-quality/4] Focusing the session field overflowed the dialog past the terminal height; added `sessionVisibleRows()` shrink-to-fit with a floor — round 1
- [code-quality/5] Lazy-scan wiring relied on unspecified Go evaluation order; sequenced explicitly and covered by `TestHandleCreatePaneSetupKey_TabToSessionFieldScans` — round 1
- [code-quality/6] `internal/tui/model.go` lost gofmt alignment; reformatted — round 1
- [code-quality/suggestion] `Truncated` was inferred from `len == MaxSessions`, mislabelling a directory holding exactly the cap; `List` now reports it — round 1
- [code-quality/suggestion] `readTitle` dropped a complete final line for a file exactly `titleScanBytes` long with no trailing newline — round 1
- [code-quality/suggestion] `applyClaudeSessions` error path left `sessionTruncated` stale — round 1
- [code-quality/suggestion] `sessions` without `prompts_cwd` rendered a permanently empty field; now fails plugin load — round 1
- [code-quality/suggestion] No paging in a list that can hold 200 rows; added PgUp/PgDn/Home/End — round 1
- [code-quality/suggestion] `liveClaudeSessionIDs` comment overstated the locking (`SnapshotState` already releases `sm.mu`) — round 1
- [code-quality/suggestion] Docs mockups showed `( )` radio markers the renderer never draws — round 1
- [rules/1] CLAUDE.md not updated for the new package / IPC pair / plugin key — added (the agent read a pre-update snapshot; re-updated afterwards for the review fixes too) — round 1
- [rules/2] Branch named `feat/…` against the repo's `feature/…` convention; renamed — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/3 + security-adjacent] No timeout/cancellation on discovery filesystem I/O — deferred rather than dismissed: filed as `techdebt/3-3-discovery-packages-have-no-io-timeout.md` because the same gap exists in `gitdiscover` and `kubediscover`, and the fix belongs in one shared pass over all three rather than in this feature (round 1)
- [code-quality/suggestion] Session cursor requires an explicit Enter while the kube field commits on cursor move — kept deliberately: resuming the wrong conversation is costlier than selecting the wrong kube context, and the collapsed summary line always shows the committed value so the state is never misrepresented (round 1)
- [code-quality/suggestion] `onSetupFieldFocused` never runs for the initially-focused field — benign today (index 0 is the CWD field for every plugin that opts into `sessions`, which now requires `prompts_cwd`); revisit if a future field arrangement puts a scanning field first (round 1)
