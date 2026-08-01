# Code Review State: quil / claude-resume-session-hijack

Last reviewed: 2026-08-01
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [greptile/P1] restore claim was not atomic — `claudeSessionHolder` released `resumeClaimMu` before the caller recorded the claim; replaced by `claimResumeSession`, which selects and records under one lock hold — round 1
- [security/H-1] `verified` bypassed `resumeSessionIDRe`, and a recorded path could vouch for an unrelated id; every candidate is now shape-checked and a path only speaks for the id it names (`filepath.Base(path) == id+".jsonl"`) — round 1
- [security/H-2] unbounded `os.Stat` on an attacker-influenced path ran before the IPC server listens; the CWD-derived probe was deleted outright and the recorded-path probe now goes through `statExistsWithinBudget` (shared blocking-FS-call budget + timeout) — round 1
- [security/M-1] chosen session id was logged verbatim into a log the F1 viewer renders unsanitized; rejects are now logged by length only and accepted ids are shape-validated first — round 1
- [security/M-2] `refreshTranscriptPath` read-modify-wrote the id file, so a Stop racing a SessionStart could restore the pre-rotation id; the refresh now writes a `<paneID>.transcript` sidecar and never touches `<paneID>.id` — round 1
- [security/L-1, code-quality/5] verified-first ordering could rank a stale located id above the live hook id; ordering is now strictly by source authority and `usableResumeCandidates` only drops candidates proven missing — round 1
- [code-quality/1] `chosen.id == ""` conflated "no recorded session" with "every candidate rejected", so the second still reached `--continue`; `claudeResumeCandidates` now returns `sawRecorded` and the rejected case starts a fresh session — round 1
- [code-quality/2] the refusal path left the refused id in `PluginState`, which a later Alt+R would pass to `--session-id`; `freshClaudeSession` now mints a new id and drops the stale transcript path — round 1
- [code-quality/3] `claudeResumeTemplate` wrote `session_id` without its `transcript_path`, desynchronising the pair `refreshPluginStateFromHooks` maintains; `recordResumeSession` writes both together — round 1
- [code-quality/6, qa/3] three pre-existing tests passed only because their fixture ids were not canonical uuids, so the probe path they name was unexercised; re-fixtured with uuids and expectations flipped — round 1
- [code-quality/8] nullable `sessionClaimFn` footgun — the parameter is never nil now, with `claimAny` as the no-occupancy stand-in — round 1
- [code-quality/9] `readHookSessionFn` errors were flattened silently; anything other than `os.ErrNotExist` is now logged — round 1
- [code-quality/11] occupancy map was rebuilt per candidate; `claimResumeSession` builds it once per spawn and walks the list — round 1
- [rules/HIGH] missing `CHANGELOG.md` entry — CI's `changelog` job hard-fails production-Go PRs without one; entry added under `## [Unreleased]` — round 1
- [rules/MEDIUM] residual `--continue` for a pane with no recorded session was untracked — filed as `techdebt/3-3-restored-pane-with-no-session-still-uses-continue.md` — round 1
- [rules/doc] `hooks-and-sessions.md` claimed a `resumeClaimMu` → `PluginMu` ordering the restore path did not actually have; the claim is now true and the rule file describes the new design — round 1
- [qa/1] "returns nil ⇒ no resume flag" was never asserted through `resolveSpawnArgs`; `TestResolveSpawnArgs_ClaimedSession_SpawnsFreshWithTogglesIntact` asserts the produced argv — round 1
- [qa/2] the real `sessionClaimFn` had no test; three tests now drive `claimResumeSession` against a real `*Daemon` — round 1
- [qa/4,5,6,7,8] untested guards — `parseSessionRecord` table, `\r\n` path rejection, sidecar isolation, stale-sidecar rejection, Stop-before-SessionStart, candidate dedupe, `resume_session_id`-only, and `usableResumeCandidates` ordering all covered — round 1
- [code-quality/10] `claudehook.ReadPersistedSessionID` has no production caller — retained deliberately as the package's id-only accessor, comment now says so plainly — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/LOW] `.gitignore` addition for `.claude/worktrees/` bundled into the fix commit — one-line hygiene needed to create the worktree the work was done in; not worth a separate PR (round 1)
- [code-quality/7] looping the occupancy check over all candidates — implemented, so this is resolved rather than dismissed; noted here only because the original suggestion framed it as optional (round 1)
