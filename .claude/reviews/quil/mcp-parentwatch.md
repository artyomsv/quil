# Code Review State: quil / mcp-parentwatch

Last reviewed: 2026-07-17
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/M1] GetProcessTimes-failure path skipped the impostor guard but still waited on the unverified handle (erred toward the leak) — now abandons the watchdog on unverifiable identity and lets stdin EOF govern (round 1)
- [rules/M1] CloseHandle error discarded without justification comment — both discard sites now carry explicit deliberately-ignored comments per go-conventions error-handling rule (round 1)
- [security/L1 + code-quality/L1] OpenProcess failure conflated parent-gone with access-denied — now branches: ERROR_INVALID_PARAMETER → exit(0); any other error → return, stdin EOF governs (a permission quirk can no longer kill a healthy bridge) (round 1)
- [code-quality/L2] Unix watchdog's "direct parent == owner" assumption implicit — documented (MCP stdio spawn model; wrapper launches would self-terminate) (round 1)
- [qa/R1] Unix reparent path had no test despite the parentWatchInterval seam — added TestWatchParentExit_OrphanExitsOnReparent (Linux-only helper-process integration test; sh intermediary spawns the re-exec'd test binary, exits after 1 s, asserts the orphan dies within the poll budget; zombie-aware liveness check) (round 1)

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/nit] TestParentHandleTrustworthy name lacks _Scenario_Expected suffix — reviewer's own verdict: subtest names carry the scenarios, matches the go-testing rule's table-driven example; not worth changing (round 1)
- [qa/2] Windows watchdog runtime path has zero CI coverage — structural (no Windows runner, mirrors the existing PTY testing gap); substituted by the scripted host e2e: parent killed with stdin write handle held open, bridge exited in 0.5 s via the watchdog log line, re-run after round-1 fixes (round 1)
