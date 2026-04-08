---
description: Dev vs production isolation for Quil. Never touch the running production daemon or its metadata in ~/.quil — always use dev mode for development and testing.
---

# Dev Environment — Quil Isolation

## Why this rule exists

Quil is a persistent workflow orchestrator. The developer of this project runs **Quil itself** as their daily driver in production mode (data in `~/.quil/`, daemon process owned by the user). When working on Quil's own codebase, any operation that touches the production daemon or its metadata is destructive — it can kill live panes, wipe workspace state, or corrupt the running session.

## Rules

1. **Never stop, kill, restart, or signal the production daemon.** The production daemon's PID file is `~/.quil/quild.pid` and its socket is `~/.quil/quild.sock`. These are **off limits** during development.

2. **Never modify, read for state, or delete files under `~/.quil/`.** That directory holds the user's live workspace, buffers, plugin configs, instances, window state, and logs for the *running* production session. Treat it as read-only from the developer's perspective — don't cat, grep, rm, mv, or write anything there.

3. **Never run these scripts during development** (they operate on the production daemon by default):
   - `./scripts/kill-daemon.sh` / `.ps1` — kills production daemon
   - `./scripts/reset-daemon.sh` / `.ps1` — wipes production `~/.quil/` state
   - `quil` (without `--dev`) — attaches to the production daemon

4. **Always use dev mode for building, running, and testing.** Dev mode stores all state in `.quil/` at the project root (already in `.gitignore`, pattern `/.quil/`). Dev mode uses a separate socket, PID file, and workspace — it coexists with production cleanly.
   - Build: `./scripts/dev.sh build` (Docker-based; host has no Go/make)
   - Run on Windows: `./scripts/quil-dev.ps1` (wrapper around `quil.exe --dev`)
   - Run on Unix: `./scripts/quil-dev.sh` (wrapper around `quil --dev`)
   - Or directly: `./quil --dev` / `./quil.exe --dev`
   - Alternative: `QUIL_HOME=/custom/path ./quil` for a custom data directory

5. **Dev-mode process management.** If a dev daemon needs to be stopped during testing, kill it by PID read from the **project-root `.quil/quild.pid`**, never from `~/.quil/quild.pid`. Do not use the provided `kill-daemon.sh`/`reset-daemon.sh` scripts unless they are verified to respect `QUIL_HOME` or the `--dev` flag (they currently target production paths).

6. **The `.quil/` directory at project root is gitignored.** Never commit it. If `.gitignore` ever loses this entry, re-add it in the same commit as any dev-mode work.

7. **Dev instances show `[dev]` in the status bar.** Use this visual marker to confirm you're working against the dev daemon, not production, before exercising any destructive test (pane destroy, tab close, workspace reset, etc.).

## Workflow

Every code change in this repo follows the same loop:

1. Edit code (`internal/…`, `cmd/…`).
2. Rebuild: `./scripts/dev.sh build`.
3. If a dev daemon from a previous iteration is running, stop it by PID from `./.quil/quild.pid` (or manually via Task Manager / `kill`).
4. Launch: `./scripts/quil-dev.ps1` (Windows) or `./scripts/quil-dev.sh` (Unix).
5. Verify `[dev]` is visible in the status bar before testing.
6. Test the change. When done, close the dev TUI — do NOT run any `kill-daemon` / `reset-daemon` helper scripts.

## What NOT to do

- `./scripts/kill-daemon.sh` during development — kills production.
- `./scripts/reset-daemon.sh` during development — wipes production state.
- `rm ~/.quil/*` or any `~/.quil/` mutation.
- `./quil` without `--dev` — attaches to production daemon.
- Reading `~/.quil/workspace.json` or any production state file for debugging — use dev mode's `.quil/workspace.json` instead.
- Running `docker` commands that bind-mount `~/.quil/` into a container.
