---
description: ConPTY, Windows console quirks, window geometry persistence, and pane spawn-size healing. Load when touching PTY code, any *_windows.go file, or console/window sizing.
paths:
  - "**/internal/pty/**"
  - "**/cmd/quil/*_windows.go"
  - "**/cmd/quild/*_windows.go"
  - "**/internal/tui/consolefix*.go"
  - "**/*_windows.go"
---

# Windows PTY

Extracted verbatim from `.claude/CLAUDE.md`. Loaded only when the files above are in play.

## ConPTY

### `internal/pty/`

Cross-platform PTY (build tags: `linux || darwin || freebsd`, `windows`). On Windows, `session_windows.go` prefers a **bundled ConPTY host** (`internal/pty/winconpty/`, charmbracelet/x/conpty vendored to route the 3 pseudoconsole syscalls through a bundled `conpty.dll` + `OpenConsole.exe`) over the inbox `kernel32.CreatePseudoConsole`, falling back to inbox if the dll is absent. The Win10 inbox conhost re-serializes claude-code's incremental input render incorrectly (the "H ello" caret gap); the newer bundled OpenConsole renders it cleanly, like Windows Terminal. `cmd/quild/main.go` calls `apty.PrepareBundledConPTY(QuilDir())` at startup → `winconpty.Extract` writes the go:embed'd binaries to `QuilDir()/conpty/<version>/` **only on Windows 10 / older** (`RtlGetVersion().BuildNumber < 22000`); Win11+ uses the inbox conhost untouched. The binaries are gitignored (`*.dll`/`*.exe`) and fetched at build time by `scripts/fetch-conpty.sh` (Microsoft.Windows.Console.ConPTY NuGet, x64) — wired into `dev.sh build`/`cross` and `.goreleaser.yml` before-hooks. See `THIRD_PARTY_LICENSES.md`.

**`childEnv` (`session_unix.go`) supplies `TERM` when the daemon's own environment has none** — `ssh -T` allocates no TTY and exports no `TERM`, so a daemon started by `quil --stdio` has none and every pane child inherits the gap; tcell-based tools (k9s, lazysql) exit 1 within MILLISECONDS, which presents as a pane that opens and instantly dies rather than as a missing variable. Set only when ABSENT (an inherited value describes the attached terminal accurately) and appended BEFORE `s.env`, so a plugin-supplied `TERM` still wins under execve's last-occurrence rule. `Start` assigns `cmd.Env` UNCONDITIONALLY now: it used to do so only when `len(s.env) > 0`, so panes with no plugin env were exactly the ones that inherited the daemon's environment verbatim, missing `TERM` included. Unix only — ConPTY children drive the console through Win32 and VT processing rather than terminfo, so introducing it there would change behaviour on a platform where nothing is broken (RD-038 asks whether Quil should instead ALWAYS set it, since Quil *is* the pane's terminal).

**That unconditional assignment then broke `PWD`, and the mechanism is worth remembering**: `os/exec` sets `PWD` from `Cmd.Dir` ONLY when `Cmd.Env` is nil (deliberate — go.dev/issue/50599 — so it cannot override a caller's value), so the panes that previously left `Env` nil were exactly the ones getting the fixup, and they silently began inheriting the DAEMON's `PWD` (ssh's login dir under `--remote`). `childEnv` now sets it explicitly, absolute, before `extra`.

**The last-wins precedence is `os/exec`'s `dedupEnv`, NOT execve** — execve passes duplicates through and glibc/musl `getenv` both return the FIRST match, so a future move to `syscall.Exec`/`posix_spawn` would silently invert plugin-vs-default precedence. Both bugs lived in `Start`'s ONE-LINE use of `childEnv` rather than in `childEnv`, so no white-box test of it could reach either — `session_unix_test.go` spawns a real child through the public `Session` API with no plugin env (the case that broke) and asks what it actually sees.

## Console mode

### Windows console-mode restore (`cmd/quil/consolemode_windows.go`, no-op `consolemode_other.go`)

OpenSSH's `ssh.exe` puts the console into VT passthrough including `DISABLE_NEWLINE_AUTO_RETURN` and restores it on exit — but `stdioConn.Close` kills it with `TerminateProcess`, and a terminated process runs no cleanup, so a bare `\n` stopped returning to column 0 and every remote diagnostic staircased off the right edge. `saveConsoleMode()` runs early in `main()` before anything spawns; `restoreConsoleMode()` runs before each post-dial diagnostic (the version-mismatch arm and the dead-link arm in `version_gate.go`, placed AFTER that arm's two order-critical `LinkErr`/`ExitCode` reads). Only the FAILURE paths were affected — a successful dial hands the terminal to Bubble Tea, which sets and restores its own modes — which made it worse, not better, since those messages are printed exactly when they must be readable. Reuses `window_windows.go`'s `kernel32` lazy DLL (same package, same build tag — declaring it again is a duplicate-symbol error). The **skip-on-failed-probe guard is the one with teeth**: a failed `GetConsoleMode` leaves `mode` at zero, and `SetConsoleMode(h, 0)` on a real console clears `ENABLE_PROCESSED_OUTPUT` and VT handling outright, turning a cosmetic indent bug into a console with no ANSI at all

## Window geometry

### Window size persistence

`~/.quil/window.json` stores cols, rows, pixel dimensions, and maximized state. Saved on TUI exit, restored on launch via platform-specific code (`cmd/quil/window_windows.go` uses Win32 `MoveWindow`/`ShowWindow`, `cmd/quil/window_unix.go` uses xterm resize sequence). Follows the same build-tag file-split pattern as `proc_unix.go`/`proc_windows.go`.

**ConPTY ghost guard**: under ConPTY hosts (Windows Terminal, VS Code) `GetConsoleWindow()` returns a hidden `PseudoConsoleWindow` — `ShowWindow(SW_MAXIMIZE)` on it makes an invisible full-screen window appear that swallows mouse clicks for every window beneath it. `realConsoleWindow()` discriminates by **window class** (`GetClassNameW`): only `"ConsoleWindowClass"` (genuine conhost) may be moved/maximized/persisted; `"PseudoConsoleWindow"` (the ConPTY ghost) returns 0 → restore and save both skip, and `saveWindowSize` carries forward the previous session's pixel/maximized values so a ConPTY session never poisons real conhost geometry.

**Do NOT gate on `IsWindowVisible`** — the ConPTY ghost has `WS_VISIBLE` set (sits at a zero rect), so `IsWindowVisible` returns true for it; the first version of this fix gated on visibility and was a no-op in real Windows Terminal sessions. Pure discriminator `isRealConsoleClass(class)` is unit-tested in `window_windows_test.go`

### Window-size poll

`sizePollTick()` (1s, started in `Init`) fires `sizePollMsg` → `sizePollProbe` — automatic recovery for the missed-WindowSizeMsg class (resize → maximize leaves the TUI at a stale size). `sizePollProbe` first runs `fixupConsoleGrid()` (`internal/tui/consolefix_windows.go` + no-op `consolefix_other.go`): legacy conhost shrinks its screen buffer with the window but NEVER grows it back on enlarge/maximize — it paints dead space and `GetConsoleScreenBufferInfo` keeps reporting the stale grid, so polling alone can't see the real size. The fixup compares the window's client pixel area (GetClientRect ÷ GetConsoleFontSize cell metrics) against the current grid via the pure `consoleGridTarget()` (consolefix.go, unit-tested) and grows buffer+window via `SetConsoleScreenBufferSize`/`SetConsoleWindowInfo` (grow-only, never shrinks a buffer axis). Then returns `tea.RequestWindowSize()` (direct `term.GetSize` syscall — no ANSI query). The redraw key reuses `sizePollProbe`. The `WindowSizeMsg` handler no-ops when the size matches both applied and pending values, so idle polls are free (no log spam, no resize IPC)

### Pane spawn-size healing

(Windows ConPTY drops resize events fired before the child reads console input — claude/node mid-boot — and never replays them): (1) daemon `resizeKick` re-applies `pane.Cols/Rows` with a 1-column jiggle on the pane's FIRST output (`streamPTYOutput`), (2) `cols`/`rows` are persisted in `workspace.json` and `respawnPanes` creates the ConPTY via `newRestoredPTY` → `apty.NewWithSize` so restored children boot at the real size, (3) TUI schedules `paneSettleRepaintMsg` ClearScreen ticks (300ms + 2s) on a pane's first live output (`PaneModel.liveOutputSeen`) to clean stale cells left by the kick-induced reflow (host font/width disagreement on Claude's logo glyphs)

