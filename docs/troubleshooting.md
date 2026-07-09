# Troubleshooting

When things go sideways, this is the first place to look.

## Table of contents

- [The daemon won't start](#the-daemon-wont-start)
- [Quil hangs, or tabs don't reopen after a restart](#quil-hangs-or-tabs-dont-reopen-after-a-restart)
- [macOS: `zsh: killed quil` after upgrading](#macos-zsh-killed-quil-after-upgrading)
- [The TUI shows a blank screen](#the-tui-shows-a-blank-screen)
- [Version mismatch — daemon won't accept the TUI](#version-mismatch--daemon-wont-accept-the-tui)
- [MCP — AI client doesn't see Quil](#mcp--ai-client-doesnt-see-quil)
- [`Ctrl+V` doesn't paste on Windows](#ctrlv-doesnt-paste-on-windows)
- [Extra space / garbled text when typing on Windows 10](#extra-space--garbled-text-when-typing-on-windows-10)
- [Pane shows ghost (dimmed border) and never goes live](#pane-shows-ghost-dimmed-border-and-never-goes-live)
- [Claude Code session doesn't resume](#claude-code-session-doesnt-resume)
- [Log files — where to look](#log-files--where-to-look)
- [Enable debug logging](#enable-debug-logging)
- [Force-stop the daemon](#force-stop-the-daemon)
- [Checking daemon + session status](#checking-daemon--session-status)
- [Reset everything](#reset-everything)

---

## The daemon won't start

Symptoms: `quil` prints `cannot connect to daemon` and exits, or the TUI hangs on attach.

1. **Check the PID file.** If `~/.quil/quild.pid` exists, the daemon may be running (or stale).
   ```bash
   cat ~/.quil/quild.pid
   ps -p $(cat ~/.quil/quild.pid)
   ```
   If `ps` shows no such process, the PID file is stale — see [Force-stop](#force-stop-the-daemon).

2. **Check the socket.** The daemon listens at `~/.quil/quild.sock` (Unix) or a named pipe (Windows). If the socket is missing but the process is alive, the daemon is mid-startup or crashed mid-bind — check the daemon log.

3. **Check `quild` is on `PATH`.** The TUI auto-spawns `quild --background` from the same directory it lives in (or from `PATH`). If `quild` is missing, install it via [Installation](installation.md).

4. **Check the daemon log:**
   ```bash
   tail -100 ~/.quil/quild.log
   ```
   Common errors: socket binding failed (permission), workspace.json deserialize error (corrupted state).

## macOS: `zsh: killed quil` after upgrading

Symptoms: after upgrading an existing install, running `quil` (or `quild`) prints `zsh: killed quil` and exits instantly. Reinstalling doesn't help.

This is the macOS kernel killing the binary at exec time, not a Quil crash. Confirm it by checking the newest crash report:

```bash
ls -t ~/Library/Logs/DiagnosticReports/quil-*.ips | head -1 | xargs grep -o '"signal":"[^"]*"\|"indicator":"[^"]*"'
```

If you see `SIGKILL (Code Signature Invalid)` / `Taskgated Invalid Signature`, you've hit it.

**Cause.** macOS caches code-signing information per inode. Older versions of `install.sh` overwrote the existing binaries in place with `cp`, which reuses the inode — the kernel's cached signature for the *old* binary no longer matches the *new* bytes, so every exec is SIGKILLed even though the binary's signature is actually valid (`codesign --verify` passes).

**Fix.** Re-run the installer — current versions install via temp file + `mv`, which gives the destination a fresh inode and clears the stale cache entry:

```bash
curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh
```

If you're stuck with an old copy of the installer, delete the binaries first so the copy lands on new inodes:

```bash
rm -f ~/.local/bin/quil ~/.local/bin/quild
```

then reinstall.

## The TUI shows a blank screen

Symptoms: `quil` runs but the screen is empty or scrambled.

1. **Resize the window.** Bubble Tea sometimes needs a SIGWINCH to repaint — drag your terminal corner.
2. **Check terminal emulator support.** Quil requires 256-colour support. Run `tput colors` — should print `256`.
3. **`TERM` env var.** Should be something like `xterm-256color`. If `TERM=dumb`, install ncurses-term and try `TERM=xterm-256color quil`.
4. **Check the client log:** `~/.quil/quil.log`.

## Version mismatch — daemon won't accept the TUI

Symptoms: a dialog says the daemon and TUI versions differ.

The TUI handshakes with the daemon on attach. The two MUST be the same version:

- **TUI is newer than daemon** — the dialog offers "Stop daemon and restart" which gracefully terminates the old daemon and auto-spawns the bundled one.
- **TUI is older than daemon** — the TUI refuses to attach. Update your TUI binary to match.

If the auto-restart fails, do it manually:

```bash
quil restart          # stop (escalating) + fresh daemon + TUI
```

## MCP — AI client doesn't see Quil

Symptoms: AI client shows zero MCP tools, or "no server named quil".

1. **Restart the client.** MCP servers are discovered at startup — config changes don't hot-reload.
2. **Check `quil` is on the client's `PATH`.** On macOS, GUI-launched apps don't inherit terminal `PATH`. Use the absolute path in your client config:
   ```json
   "quil": {
     "command": "/Users/you/.local/bin/quil",
     "args": ["mcp"]
   }
   ```
3. **Run the bridge by hand** to see startup errors:
   ```bash
   quil mcp
   ```
   It should print nothing on stdout (that's where JSON-RPC goes) but you'll see daemon-connection errors on stderr.

See [MCP → Troubleshooting](mcp.md#troubleshooting) for more.

## `Ctrl+V` doesn't paste on Windows

Symptoms: pressing `Ctrl+V` in a Quil pane on Windows does nothing.

Windows Terminal captures `Ctrl+V` for its own paste action **before the TUI sees it**. The fix is documented but non-obvious:

- Press **`F8`** instead — it's a paste alias with no key-conflict on Windows.
- Or use **`Ctrl+Alt+V`** — another alias.

You can also tell Windows Terminal to forward `Ctrl+V` by remapping it in `settings.json`, but `F8` is the friction-free path.

## Extra space / garbled text when typing on Windows 10

Symptoms: typing in an AI pane (e.g. Claude Code) on **Windows 10** shows an extra space after the first character — `Hello` renders as `H ello` — that self-corrects when the line wraps or you press Enter.

Cause: the Windows 10 inbox console host (`conhost.exe`) mis-renders some TUIs' incremental input. Quil fixes this automatically by bundling Microsoft's newer **OpenConsole** and hosting panes through it on Windows 10 (see [Architecture → ADR-25](architecture.md#adr-25-bundled-openconsole-conpty-host-on-windows-10)). Windows 11 is unaffected and uses the inbox host.

If you still see it on Windows 10, the bundled host didn't load — check the daemon log (`%USERPROFILE%\.quil\quild.log`) for `conpty:` lines:

- `conpty: Windows build NNNNN (<11); extracting bundled OpenConsole` — expected on Win10; the host is active.
- `conpty: bundled host unavailable (...)` — extraction/load failed and Quil fell back to the inbox host. Ensure `%USERPROFILE%\.quil\conpty\` is writable and not blocked by antivirus, then restart the daemon (`quil daemon restart`).

## Pane shows ghost (dimmed border) and never goes live

Symptoms: a restored pane border stays dimmed with a `restored` label and no fresh output appears.

The pane's underlying process died before producing live output. Check `~/.quil/quild.log` for spawn errors near the pane id. Common causes:

- The plugin binary (`claude`, `opencode`, `ssh`, …) isn't on `PATH`
- The CWD no longer exists (e.g., you deleted the project directory)
- The plugin's resume args reference a stale id (e.g., `--session <gone>`)

Close the pane (`Ctrl+W`) and create a fresh one (`Ctrl+N`).

## Claude Code session doesn't resume

Symptoms: on restart, `claude` starts a new conversation instead of resuming the old one.

Quil tracks Claude session-id rotation via a `SessionStart` hook. If the hook didn't run:

1. **Check the hook script exists:**
   ```bash
   ls -la ~/.quil/claudehook/
   ```
   Should contain `quil-session-hook.sh` and `quil-session-hook.ps1`. If missing, restart the daemon — it re-installs on every start.

2. **Check the recorded session id:**
   ```bash
   cat ~/.quil/sessions/<pane-id>.id
   ```
   Empty or missing means the hook never fired. Look at `~/.quil/claudehook/hook.log` for errors.

3. **`QUIL_HOME` characters.** The hook installer rejects shell-unsafe characters in `$QUIL_HOME`. If you set `QUIL_HOME=/path/with"quote/` the daemon refuses to install the hook (see warning in daemon log).

For OpenCode the equivalent files are under `~/.quil/opencodehook/` and `~/.quil/sessions/opencode-<pane-id>.id` — see [Features → OpenCode session-id tracking](features.md#opencode-session-id-tracking).

## Log files — where to look

| File | What's in it |
|---|---|
| `~/.quil/quil.log` | TUI client log (input handling, dialog state, IPC send/receive) |
| `~/.quil/quild.log` | Daemon log (pane lifecycle, IPC dispatch, snapshot timings, spawn commands) |
| `~/.quil/mcp-logs/<pane-id>.log` | Per-pane MCP interaction log (tool name, timestamp, sanitized detail) |
| `~/.quil/claudehook/hook.log` | Errors from the Claude Code SessionStart hook |
| `~/.quil/opencodehook/hook.log` | Errors / breadcrumbs from the OpenCode JS plugin |
| `~/.quil/quild.stderr.log` | Daemon panics and SIGQUIT goroutine dumps (anything the Go runtime writes to stderr) |

From inside the TUI: `F1 → View client log` / `View daemon log` / `View MCP logs` opens a read-only viewer with `Alt+Up` / `Alt+Down` for paged navigation.

**Wedge diagnostics:** if the daemon ever stops responding, search `quild.log` for `WATCHDOG` — when no workspace snapshot completes for 2 minutes, the daemon writes a full goroutine stack dump there. Attach that dump to a bug report; it pinpoints the blocked code path exactly.

## Enable debug logging

Two paths:

**Persistent** — edit `~/.quil/config.toml`:

```toml
[logging]
level = "debug"
```

Takes effect on the next launch.

**One-off** — run the `quil-debug` binary built by `./scripts/dev.sh build`:

```bash
./quil-debug
```

Debug builds attach to the production `~/.quil/` daemon and emit verbose logging. Don't use them as your daily driver — they're noisy.

## Quil hangs, or tabs don't reopen after a restart

Symptoms: the TUI freezes (no pane output, keys ignored), or after quitting and relaunching, `quil` connects but shows an empty workspace with no tabs.

Both are signatures of a wedged daemon: the process is alive and accepts connections, but its internals are stuck, so it never delivers your workspace to the TUI. Your state is safe — the daemon snapshots `workspace.json` every 30 seconds. Recover with one command:

```bash
quil restart
```

It prints which environment it's operating on (production `~/.quil` or dev `QUIL_HOME`), stops the daemon with bounded escalation — graceful IPC shutdown (final snapshot) → SIGTERM → force-kill, each tier with a timeout so a deadlocked daemon can't stall it — cleans up stale pid/socket files, starts a fresh daemon, and opens the TUI. Your tabs and panes respawn from the last snapshot.

`quil daemon restart` does the same without launching the TUI.

A related single-pane symptom: an AI pane (e.g. Claude Code after a context compaction) stops reacting to keystrokes while everything else works. The process has stopped reading its stdin. The daemon drops the unread keystrokes (you'll see a **"Pane not accepting input"** warning in the notification sidebar, `Alt+N`) instead of freezing — restart that one pane (close with `Ctrl+W` and recreate, or use the MCP `restart_pane` tool) to recover it.

## Force-stop the daemon

`quil daemon stop` uses the same bounded escalation as `quil restart` (graceful IPC → SIGTERM → force-kill) and cleans up the pid/socket files, so it works even against a wedged daemon. Manual fallback, if you ever need it:

```bash
# Read the PID and SIGTERM
kill "$(cat ~/.quil/quild.pid)"

# Force-kill if that didn't work
kill -9 "$(cat ~/.quil/quild.pid)"

# Clean up stale PID file and socket
rm -f ~/.quil/quild.pid ~/.quil/quild.sock
```

The repo also ships kill scripts: `./scripts/kill-daemon.sh` (Unix) and `./scripts/kill-daemon.ps1` (Windows). **These target the PRODUCTION daemon** — don't use them while testing dev builds.

## Checking daemon + session status

`quil status` reports whether the daemon is running and prints a live snapshot
of the workspace:

```
quil status            # human-readable tree
quil status -v         # also show each pane's working directory
quil status --json     # machine-readable, for scripts/CI
```

Exit codes: `0` healthy, `1` daemon not running, `2` daemon running but not
responding (wedged). `quil daemon status` is an alias.

Add `--dev` (or set `QUIL_HOME`) to inspect a dev instance instead of production.

## Reset everything

If state is corrupted beyond recovery:

```bash
# Stop the daemon
quil daemon stop 2>/dev/null || true

# Nuke all state (WARNING: drops workspaces, ghost buffers, plugins, instances, notes, MCP logs)
rm -rf ~/.quil/

# Re-launch — the daemon rebuilds default plugins and an empty workspace
quil
```

Your `~/.quil/plugins/*.toml` files are part of "state" — if you customized them, back up the directory before nuking.
