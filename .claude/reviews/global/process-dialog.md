# Code Review State: global / process-dialog

Last reviewed: 2026-08-20
Rounds completed: 1

Covers PR #179 (branch `feat/process-dialog`). The ten findings recorded in
`frame-cost-and-memory.md` under "Removed with the feature" were fed in as prior
state, because that section says to re-raise them only if the dialog returns —
and it has. All ten were verified as resolved by three of the four agents
independently; they are not repeated here.

## Resolved (fixed in code; do not re-raise)

- [security/HIGH] `ProcNode.Depth` reached `strings.Repeat("  ", depth+2)` unclamped. It is a plain int off the socket and Repeat PANICS on a negative count, so a daemon answering `"depth": -3` killed the TUI the moment a pane was expanded — the dialog's own happy path, and in remote mode the daemon is a machine the user may not control. Verified empirically (`depth=-3` panics, `-2` does not). `clampDepth` + tests — round 1
- [code-quality/CRITICAL] The dialog had no height bound and no scroll window. `lipgloss.Place` does not clip, so the 33-tab workspace this repo records drew ~42 rows past a 40-row terminal: the footer was gone and the cursor moved into rows never painted. `procWindow` (the `historyWindow` shape, called by both the cursor sync and the renderer), pgup/pgdown/home/end, an off-screen row count, and a height assertion test — round 1
- [code-quality/IMPORTANT] `procLine` budgeted in CELLS and padded in RUNES. `truncateToWidth` measures cells; `fmt`'s `%-*s` pads runes, so a CJK or emoji name overshot and wrapped every row — the exact 89-into-86 defect the predecessor was pulled for. Invisible because every fixture was ASCII. `padCell`/`padCellRight` plus an unconditional clamp of the assembled line, since the column arithmetic has a floor and cannot satisfy a narrow terminal alone — round 1
- [security/MEDIUM] Numeric columns were unbounded while string columns were not. A PID of `math.MaxInt64` is 19 characters into a 7-cell column and `%.0f` on 1e308 produces ~310. Now bounded, with NaN/Inf handled — round 1
- [code-quality/CRITICAL + security/MEDIUM] Enumeration ran on the IPC dispatch goroutine at three sites (`Renew`'s first collect, `resolveKillTarget`, `TermPass`). `handleConn` is sequential per conn, so a parked handler stops that client's keystrokes to every pane — the documented 2026-06-11/12 wedge shape. On Darwin the graceful pass forks `ps` PER NODE, so the "completes in microseconds" comment was false there. All moved to worker goroutines behind a single-flight, per `handleClaudeSessionsReq` — round 1
- [correctness] `aliveWithStart` reported a ZOMBIE as alive on Linux. `/proc/<pid>/stat` outlives a process until its parent reaps it, start time intact, and a subtree sweep manufactures that state by killing parents and children together. `Escalate` SIGKILLed corpses and counted them as forced; a zombie under a live parent rendered as killable. Found by the new real-process tests, not by inspection — round 1
- [security/LOW] The sweep terminated the descendants of a node whose own start time was unreadable. `Build` keeps such a link deliberately (dropping it hides a real child) and justified it by saying the kill path refuses an unknown start — true of the target, false of its descendants, whose own start times are readable. `unverifiableSubtrees` — round 1
- [code-quality/IMPORTANT] The quil section collected `Version`, `UptimeMS` and `ExeName` and rendered none of them, so it could not show the stale bridge that motivated it. The whole duration-not-timestamp uptime design was dead at the render layer. Also adds column headers and ▸/▾ expand indicators (a regression against the memory dialog) — round 1
- [code-quality/IMPORTANT] A treeless status-bar response replaced the dialog's tree-bearing one and cleared a single-flight it did not answer — both ride the same message. The response now echoes `WithTrees` rather than the client inferring it from a populated field — round 1
- [code-quality] `resp.CPUSupported` was hardcoded true, claiming CPU support on a platform with no source while also footnoting "CPU here is a kernel average". Derived from the platform reading — round 1
- [security/LOW] `ClientHelloPayload` strings were retained unbounded per connection and copied into every response; one conn holding a multi-megabyte Role would make the response fail to marshal, freezing the dialog and the status-bar total for EVERY client. Capped at 64 bytes on a rune boundary — round 1
- [code-quality/IMPORTANT] Any enumeration failure was reported as `refuseUnsupported`, telling a macOS user whose `ps` timed out that their platform lacks a feature they used a minute ago. Split via `errors.Is(err, ErrUnsupported)` — round 1
- [security/LOW] `applyKillProcessResp` lacked the dialog guard its sibling has, so a response landing after Esc surfaced a notice on the next open — round 1
- [qa/HIGH] The kill confirm's ACCEPT branch was untested: replacing `m.sendKillProcess()` with `nil` left the whole suite green. The prior round's finding asked for the kill path to be reachable from its call site, and the test written for it stopped at OPENING the confirm. Now driven end to end against a `fakeSender`, modelled on `TestHandleConfirmKey_StopDaemonYSendsAndQuits` — round 1
- [qa/HIGH] `handleResourceReportReq`, `handleClientHello` and `handleKillProcessReq` were wired into the dispatch switch but never driven through it; all three arms could be disconnected with `internal/daemon` green. Now driven through `handleMessage` asserting observable side effects — round 1
- [qa/MEDIUM] `kill_linux.go` had no test file at all, though Linux is the one platform CI can exercise against real processes. Covers pidfd signalling, the pre-5.3 fallback, start-time refusal, nonsense PIDs, the zombie case, and a full `Sweep` through `DefaultKillOps` — round 1
- [qa/MEDIUM] `parsePSStart` had no direct test despite carrying its own field arithmetic (`fields[:5]` vs `parsePSLine`'s `fields[2:7]`) and being the entire Darwin kill path's identity check — round 1
- [qa/LOW] `TestKillProcess_ThroughUpdate` claimed to drive the path through `Update` and called the handler directly. Routed through `handleDialogKey`: deleting its `dialogProcesses` arm now fails four tests instead of none — round 1
- [code-quality/LOW] `respondTo` panicked on a nil conn, taking down the dispatch goroutine rather than dropping one response — round 1
- [code-quality/LOW] Six comments referenced symbols deleted with `memory.go` (`handleMemoryDialogKey`, `refreshMemory`, `renderMemoryDialog`, `memoryDialogState`, and the About index map still reading `3:Memory`) — round 1
- [code-quality/LOW] `resolveKillTarget` returned a root no caller used (`_ = root`); `table_windows.go` had a no-op `if err != nil { _ = err }` branch; the Darwin kill comment claimed a "microseconds" identity window when a `ps` fork sits inside it — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)

- [rules/LOW] `cmd/quil/hello.go`'s package-level `var processStart = time.Now()` is global mutable state against `go-conventions.md`. Written once at package init and never reassigned, so it has neither of the properties the rule exists to prevent (races, hidden coupling); the alternative is threading a process-start timestamp through all ten dial sites. Same shape as the adaptive-scrollback atomics dismissal on PR #177. Raised by the reviewer itself as "not blocking" — round 1
- [qa/informational] One `-race` run of `internal/tui` in five failed with no captured test name, output ending `ipc recv: unknown type "test-inert"` — a string from `broadcast_echo_test.go`, unrelated to this feature, not reproducible in three follow-up runs. Recorded rather than fixed: 1/5 is not enough to call a race, and it is pre-existing — round 1
- [qa/informational] `procCollector.Renew()`'s ticker goroutine is observed starting (the dispatch test asserts `running` flips and the deadline is set) but nothing watches it complete a SECOND pass. Testing a real ticker loop costs wall-clock in every run for a loop whose body is already covered by `collect()`'s own tests — round 1

## Process notes

- Two agents mutating one worktree caused a transient full-suite failure: a run of mine caught QA's in-flight `Depth >= 2` → `>= 1` mutation and reported it as my own regression. Both agents restored correctly. Verify the tree is clean before trusting a red result when a QA agent is active.
- A directory-wide `gofmt -w ./internal/daemon/` marked ~90 files modified through CRLF alone and made one unrelated content change. Format only the files you touched.
