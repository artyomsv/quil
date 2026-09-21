---
name: handstart-osc7770-taint
description: OSC 7770 hand-start markers are pane-controlled; QUIL_INTERCEPT_TOKEN is inherited by every descendant of a terminal pane's shell, and zsh's `read -t N -k M` is NOT a read deadline
metadata:
  type: project
---

Everything in an OSC 7770 marker (`internal/daemon/handstart.go`) is attacker-shaped
output from the pane's PTY, gated only by `QUIL_INTERCEPT_TOKEN`. That token is
exported into the shell's ENVIRONMENT, so **every descendant process of a terminal
pane's shell holds it** — a build script, a hand-started agent, anything the user
runs. Treat "holds the token" as "runs code in a terminal pane", not "is the user's
keyboard".

Marker payload bytes that reach further than they look:
- `Args` → `pane.InstanceArgs` → REPLACE `Command.Args` → `exec`, and are
  **persisted in workspace.json** (`instance_args`) and silently re-applied on
  every later restart. Nothing in `ipc.PaneInfo` exposes them, so there is no UI
  surface to notice them.
- `Name` and `EnvNames` are unvalidated and reach notification cards; the TUI's
  `sanitizeRemoteText` at the render site is what makes that safe — do not assume
  the daemon bounded them.
- `CWD` reaches `filepath.EvalSymlinks`/`os.Stat` and the claude session store.

**`p.Command.Cmd` being fixed is not a bound on what runs.** `claude --mcp-config
'{...stdio server...}'` / codex `-c` turn the fixed binary into an arbitrary-command
launcher, and `handStartArgShapeOK` permits both.

Measured 2026-09-20 (real PTYs, docker): **zsh's `read -t N -k M` is an input
AVAILABILITY TEST, not a read deadline** — once one byte is available it blocks
indefinitely for the remaining M-1. bash's `read -N M -t N` is a true timeout.
Also measured: SIGINT during `read` in an interactive shell aborts the function, so
any `stty -g` / `stty -echo` pair without a trap leaves the terminal echo-off.

**Why:** this feature is the first place in quil where PTY OUTPUT drives a spawn,
and the token's stated boundary ("equivalent to the 0600 socket") is easy to read as
stronger than it is.

**How to apply:** when reviewing `internal/daemon/handstart*.go`,
`internal/shellinit/scripts/*`, or anything new that scans PTY output for a control
channel, start from "a process in the pane wrote this" and check the argv's
persistence and UI visibility, not just its shape. Related: [[osc7-cwd-taint]],
[[remote-daemon-string-taint]].
