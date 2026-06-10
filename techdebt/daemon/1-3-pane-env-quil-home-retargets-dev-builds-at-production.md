# Pane env QUIL_HOME silently retargets dev builds at production

| Field | Value |
|-------|-------|
| Criticality | Critical |
| Complexity | Medium |
| Location | `internal/daemon/daemon.go` (claudeHookSpawnPrep / opencode spawn env), `cmd/quild/main.go:28`, `cmd/quil/main.go` (dev-mode gate) |
| Found during | debugging (ConPTY ghost window investigation, 2026-06-10) |
| Date | 2026-06-10 |

## Issue

The daemon exports `QUIL_HOME=<production home>` into every pane's PTY environment so the
native hook subcommand (`quild claude-hook`) and the opencode plugin can find the sessions/events
directories. That variable is inherited by **every** process started inside a pane — including
quil's own dev builds.

The dev-mode gate in both binaries is:

```go
if buildDevMode == "true" && os.Getenv("QUIL_HOME") == "" { ... }
```

So `quil-dev.exe` / `quild-dev.exe` launched from a shell inside a production quil pane (the
normal "AI-native" workflow — Claude Code developing quil from within quil) silently target
**production** `~/.quil/` instead of the project-root `.quil/`.

Observed blast radius in one real incident: a `quild-dev.exe --background` started from a pane
overwrote the production `quild.pid` and re-bound/replaced the production `quild.sock`, leaving
the live daemon unreachable for new clients (graceful restart required).

## Risks

- Dev daemon clobbers production PID file and socket → production daemon unreachable, panes lost on next attach.
- Dev TUI attaches to production daemon and writes dev log lines / window state into production files.
- The failure is silent — nothing warns that "dev mode" is operating on production state.
- Violates the project's own production-isolation rule with zero misuse required.

## Suggested Solutions

1. **Rename the hook-path env var** (preferred): pass `QUIL_HOOK_HOME` to pane children; `RunHook`
   and the opencode plugin read it (fall back to `QUIL_HOME` for one release). Pane children then
   no longer inherit a production-pointing `QUIL_HOME`.
2. Dev builds ignore inherited `QUIL_HOME` when it equals the default production dir, or require
   `QUIL_HOME` to be explicitly different from `QuilDir()` default — warn loudly otherwise.
3. Belt-and-suspenders: `quild` refuses to start when the PID file points at a live process
   (see `1-2-quild-no-single-instance-guard.md`).
