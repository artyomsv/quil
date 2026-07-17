# Code Review State: pty / win10-conpty-bundle

Last reviewed: 2026-06-18
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/H1] fetch-conpty.sh pins conpty.dll + OpenConsole.exe by SHA256 (build-time supply-chain gate) — round 1
- [security/M1] embed_windows.go upToDate uses SHA256 vs embedded bytes, not size (self-heals corrupt files) — round 1
- [security/L1] extraction uses 0o700 dir / 0o600 files — round 1
- [security/L2] embed_windows.go rejectSymlink on extract dir + dest files (Lstat) — round 1
- [code-quality/M1] bundled_windows.go loadBundled memoizes success only, retries on failure (was sync.Once caching transient errors) — round 1
- [code-quality/M2] bundledClosePseudoConsole guard documented as a synchronized fast no-op (cannot early-return on a live handle) — round 1
- [code-quality/L3] coordToUintptr uses pure arithmetic instead of unsafe reinterpret cast — round 1
- [code-quality/L6] bundledDLLPath returns "" (not relative "conpty.dll") when os.Executable fails → clean inbox fallback — round 1
- [code-quality/L7] session_windows.go per-spawn conpty backend log uses logger.Debug, not log.Printf — round 1
- [rules/M1] removed legacy "// +build windows" from conpty_windows.go + exec_windows.go — round 1
- [rules/M2] added winconpty_test.go (TestCoordToUintptr, TestUpToDate*, TestExtract*) — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)

## Notes
- [rules/M3] "CLAUDE.md not updated" was a FALSE POSITIVE — CLAUDE.md was updated in this change set; it was excluded from agent scope via the *.md filter, so the agent couldn't see it. No action needed.
- GOOS=windows `go vet` reports "possible misuse of unsafe.Pointer" in the VENDORED conpty_windows.go / exec_windows.go (verbatim from charmbracelet/x/conpty upstream) — benign syscall/env-block patterns; the project's gate (`go vet ./...` on Linux) excludes Windows files and is clean. Not patching vendored code.
