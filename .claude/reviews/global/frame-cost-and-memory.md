# Code Review State: global / frame-cost-and-memory

Last reviewed: 2026-08-20
Rounds completed: 1

Covers PR #177 (branch `perf/frame-cost-and-memory`). The sibling
`performance.md` covers PR #175, which is merged; its findings concern code no
longer in this diff.

## Resolved (fixed in code; do not re-raise)

- [code-quality/CRITICAL + security/correctness] The F1 → Processes dialog's central capability was dead on BOTH platforms. Windows: the enumerator filled `Cmdline` from `QueryFullProcessImageNameW`, an image PATH, so `IsBridge`'s `mcp`-token match never fired and every bridge classified as the TUI — and a comment claiming the subcommand comes from `PROCESSENTRY32.szExeFile` was false. Unix: an orphan reparents to PID 1, always present and always older, so `hasLiveParent` never reported dead. Verified directly (`orphans=0` on both shapes). **Feature REMOVED from the PR** rather than patched — round 1
- [code-quality/CRITICAL] `IsBridge` tested the WHOLE first field for the substring `"quil"` (siblings `IsDaemon`/`isQuilTUI` basename first). This repo's own path contains it, so `/home/quil/.venv/bin/python -m mcp` classified as quil's own orphaned bridge — once the Unix half was fixed the dialog would have offered to kill someone else's MCP server. Removed with the feature — round 1
- [code-quality/HIGH] The scrollback budget was divided by ONE daemon's pane count: `applyWorkspaceState` receives a single host's state and `SetPaneCount` kept the maximum, so three 15-pane hosts published 15 and every pane got triple its share of a budget that is a property of this process. Counts are now tracked per destination and summed (`setDestPaneCount`), pinned by two tests — round 1
- [code-quality/HIGH + qa/MEDIUM-HIGH] `TestBlockIsWidth_OnlyChecksFirstAndLastLine` used `"short"` (5 cells) as its "ragged" fixture, so the block was rectangular and the test logged "happens to agree" while asserting nothing — it named a hole it did not exercise. Now uses a genuinely interior-ragged block and asserts the divergence for both joins — round 1
- [qa/MEDIUM-HIGH] Inverting EITHER width comparison in `blockIsWidth` survived the whole equivalence suite while producing unpadded output: every ragged case paired a malformed block with a uniform partner, and since both must pass the guard, the partner's correct `false` masked the wrong `true`. Added the masking shape (both blocks matching on one sampled line only) to both tables; each mutation is now caught by three tests — round 1
- [code-quality/HIGH] The joins' contract claimed "slower, never wrong". False for a block ragged in its INTERIOR — unpadded vertically, right column shifted left horizontally. Comments in `joinfast.go` and `.claude/rules/tui-rendering.md` corrected to "wrong only for a block nothing in the frame produces" — round 1
- [code-quality/IMPORTANT] `TestFrameBlocks_AreRectangular` checked one geometry and one UI state, while being the test that defends the whole premise. Replaced by `TestFrameBlocks_AreRectangularAcrossGeometries`, covering the same 3 tab counts × 3 geometries the equivalence test uses — round 1
- [rules/HIGH] `.claude/CLAUDE.md` claimed `dev.sh cross` was the Windows half's only gate. `dev.sh build` cross-compiles `GOOS=windows ./cmd/quil` six times on every ordinary build. Third fabricated infrastructure claim across these PRs; corrected, then moot when the package was removed — round 1
- [rules/MEDIUM] `docs/configuration.md` listed the `scrollback_lines` default as `10000` while its own prose and example config said `0`/adaptive — round 1
- [rules/MEDIUM] `internal/config/config.go`'s `ScrollbackLines` comment said "0 (unset) keeps the shipped default", invalidated by this PR's own change to adaptive — round 1
- [rules/MEDIUM] The new "Frame assembly" rule section was appended under `## Navigation and selection` instead of `## Rendering` — round 1
- [rules/MEDIUM] `_ = proc.Kill()` had no justifying comment; added, then removed with the feature — round 1
- [rules/LOW] `blockIsWidth`'s doc named `TestJoinVerticalWidth_MatchesLipgloss` as the real-frame test; that one uses synthetic table cases. Corrected to name `TestFrameJoins_MatchLipglossOnRealFrames` — round 1
- [rules/LOW] `joinfast_test.go` comments cited `model.go:4100`/`:4102`, drifted by later commits on the same branch. Replaced line numbers with call-site descriptions — round 1

## Removed with the feature (re-raise only if the dialog returns)

These were valid findings against `internal/procscan` / `internal/tui/processes.go`,
which this PR no longer contains. They are recorded so a follow-up starts from
them rather than rediscovering them.

- [code-quality/IMPORTANT] The kill confirm had no `case confirmKindKillProcess` in the confirm renderer, so it fell to `default:` and rendered `Close kill-process "…"?` with a footer promising Enter while the handler required `y`
- [code-quality/IMPORTANT] Esc from the kill confirm returned to `dialogNone` rather than `dialogProcesses`, disagreeing with its own accept path
- [code-quality/IMPORTANT] `applyProcessesScan` wrote `dialogCursor` regardless of which dialog was open, so a scan landing after Esc moved the About cursor. The in-repo precedent (`applyMemoryReport`) keeps its own cursor and guards on the dialog
- [code-quality/IMPORTANT] `refreshProcesses` had no single-flight, unlike the daemon's browse/discover dialogs it cites as precedent
- [security/MEDIUM] `processLabel` was the one value on the path skipping `sanitizeRemoteText`, and was unbounded; `%q` masked escape injection incidentally, and the natural rewrite to `%s` would have made it live
- [security/MEDIUM + code-quality] `killProcess` rebuilt its row list by hand and omitted the orphans-first sort `refreshProcesses` applies
- [security/LOW] Linux `starttime` overflows int64 past ~2.9 years of uptime, after which `hasLiveParent` could report a FALSE orphan — the direction the code says must never happen
- [security/correctness] Every process row was 89 cells against an 86-cell content budget, so every row would have wrapped and doubled the dialog height
- [qa/HIGH] The entire kill path was untested: disabling the `confirmKindKillProcess` branch left the whole suite green
- [qa/MEDIUM] `isQuilTUI`'s subcommand loop was untested; inverting it survived

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)

- [rules/LOW-informational] The adaptive-scrollback package-level atomics are global mutable state, against `go-conventions.md`'s "no global mutable state". They follow an explicitly-cited codebase precedent for the identical shape (`version.SetUpdatesEnabled`, `Daemon.clientCWD`'s `atomic.Pointer`) for the same reason: a process-wide answer needed at a dozen call sites with no config in hand. Recorded by the reviewer as "flagging for visibility only" — round 1
- [rules/informational] `b98333d` was committed while `test-race` was red, fixed in `4ae4b12`. Real, and disclosed in the PR body. Not a convention violation under this repo's squash-merge workflow — the intermediate state never reaches `master` — but recorded because committing on red was the author's process failure, not a workflow artifact — round 1
- [code-quality/suggestion] `blockIsWidth` measures a single-line block twice. A `ContainsRune(s, '\n')` fast path would avoid it; not taken, because the saving is one width call on the tab bar per frame against added branching in the hottest correctness-critical helper in the file — round 1
