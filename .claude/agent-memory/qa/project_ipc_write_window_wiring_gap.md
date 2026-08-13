---
name: project_ipc_write_window_wiring_gap
description: internal/ipc's newConn → writeDeadline wiring is pinned by TestNewConn_UsesTheProductionWriteWindow; it had zero coverage until mutation testing found it (2026-08-11)
metadata:
  type: project
---

`internal/ipc/server.go`: `newConn` delegates to `newConnWithWriteWindow(raw, writeDeadline)`.
Every regression test in `conn_writestall_test.go` constructs conns through
`newConnWithWriteWindow(raw, shortWindow)` so it can exercise a stall in
milliseconds — which means none of them touches the production default.

**The gap is now CLOSED** by `TestNewConn_UsesTheProductionWriteWindow`, which
asserts `newConn(pipe).writeWindow == writeDeadline` and fails when `newConn` is
mutated to a 1 s window. Keep that test: without it the whole `internal/ipc`
suite stays green while production runs a test-sized deadline.

**Why it is worth remembering anyway:** the gap was invisible from the test file
— four tests with confident names all covering the behaviour and none covering
the wiring — and only mutation found it. A seam introduced for testability
creates exactly this hole: the tests use the seam, so the real call site becomes
the one path nothing exercises. Look for the same shape wherever a constructor
gains a `...With<Knob>` variant.

**How to apply:** when reviewing a change that adds a test-only constructor
seam, ask what still pins the production caller, and mutate it to check.
Related: [[project_test_tooling]] for how mutation verification runs here
(Docker, no local Go; edit, run `-count=1`, restore, confirm `git diff` empty).
