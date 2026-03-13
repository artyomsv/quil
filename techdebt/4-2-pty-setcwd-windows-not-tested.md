# PTY SetCWD not covered by tests on Windows

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Small |
| Location | `internal/pty/session_windows.go` — `SetCWD` and `Start` with `attr.Dir` |
| Found during | QA pass for M2 persistence feature |
| Date | 2026-03-13 |

## Issue

`session_unix_test.go` covers `Start`, `Read`, `Resize`, and `Pid` on Unix PTY sessions but there is
no corresponding `session_windows_test.go`. The `SetCWD` method (added in M2) is exercised by the
daemon's `respawnShells` path but has no direct unit test on the Windows implementation.

The unix tests use a `//go:build linux || darwin || freebsd` tag and run in Docker (Linux), so
the Windows code path is never exercised in CI.

## Risks

- Regressions in `winSession.SetCWD` or the `syscall.ProcAttr{Dir: s.cwd}` plumbing could go
  undetected — shells would respawn in the wrong working directory silently.
- Any future refactoring of the ConPTY `Spawn` call has no safety net.

## Suggested Solutions

1. Add `session_windows_test.go` with `//go:build windows` that tests `SetCWD` sets the field and
   that `Start` propagates `Dir` into the `ProcAttr`. Mock or stub `Spawn` if needed to avoid
   requiring a live ConPTY in CI.
2. Add a Windows runner to the GitHub Actions matrix (a `windows-latest` job) to execute the
   Windows-tagged tests natively, in addition to the existing Linux Docker runner.
3. Accept the gap for now and document it — the `SetCWD` logic is a one-liner
   (`s.cwd = dir`) with trivially low risk; cover it when a Windows CI runner is set up.
