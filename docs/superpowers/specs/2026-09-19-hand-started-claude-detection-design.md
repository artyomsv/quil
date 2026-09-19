# Hand-started agents in terminal panes — design

**Date:** 2026-09-19 (revised 2026-09-20 ×2: launch-time interception primary; adversarial review folded in)
**Status:** Draft, awaiting review
**Origin:** GitHub issue #221 — four Claude conversations lost after a reboot
**Verified against:** `master` at v1.74.0 + #222 + #223 (`2b9044c`)

> **On line references.** Line numbers are for the tree named above and drift as it
> moves; function and symbol names identify every site. Anything marked *(guess)* was
> not measured in this repo and appears again under "Live checks". The second revision
> re-verified every citation the review disputed; where a claim was written from
> reasoning rather than the code, this revision says what the code does instead.

## Summary

A user who types `claude`, `codex` or `opencode` into a **terminal** pane gets none of
what a typed AI pane gets — no hook, no session-id tracking, no resume — and today Quil
says nothing. This spec makes the shell integration **intercept** that command before it
runs and hand it to the daemon, which respawns the pane as the proper AI pane carrying the
user's own command line. Nothing is killed, because nothing has started. Four lesser
responses cover what interception cannot reach:

| Priority | Path | When | What happens | Destructive? |
|---|---|---|---|---|
| 1 | **Intercept** (Part B) | The shell's `claude`/`codex`/`opencode` function fires, before any exec | The daemon converts the pane in place: same pane id, same leaf, `claude-code` type, the typed arguments, `--settings` + hooks, `--resume` when the user asked for one | No — the process never starts |
| 2 | *Post-exec kill* | — | **Rejected** (§B.9); kept only as the Windows fallback if the pwsh stdin read fails its live check | Yes, by ≤ 50 ms |
| 3 | **Adopt** (Part C) | An agent is already running: interception answered `run`, or a session was resolved later from disk | The pane records the session id Quil read off disk and is re-typed so a restart resumes it. The process is untouched and unaware | No |
| 4 | **Convert mid-session** (Part D) | `Alt+R` on an adopted pane | Kill-and-respawn through the existing restart path; consent carries the in-flight risk | Yes, by explicit user action |
| 5 | **Notify** (Part E) | Nothing above applied | One sidebar card: the session is untracked and here is what to do | No |

**Coverage, stated up front.** Interception reaches a command typed at an interactive
bash, zsh or PowerShell prompt in the foreground, resolved through the shell's own
lookup. It does not reach: fish (Quil injects nothing there — `shellinit.Configure`
returns nil, `internal/shellinit/shellinit.go:81-83`, and "native" in
`.claude/CLAUDE.md:194` means only that fish emits OSC 7 itself, `docs/features.md:119`);
`command claude`, `env claude`, `/abs/path/claude`, `npx …`, `xargs`, `nohup`, `sudo`,
`make` targets, vim's `:!claude`, anything in a script or subshell, and any shell whose
user already defined a `claude` function (§A.4). Those run as typed; Part C adopts a
claude among them, Part E says so for the rest. Fish users get **nothing** from this
feature, not even the card, and the docs must say so rather than imply otherwise.

Interception is generic across the three agents because it carries the command line
verbatim. Adoption (3) is claude-only in v1 because only claude has an on-disk session
store indexed by directory. Either 1 or 3 alone closes #221.

Delivered as one PR on `feat/hand-started-agents`. See "Files touched".

## Invariant: Quil never attaches to a running AI process

Every path below is either "the process never started" or "the process is killed and
respawned through the ordinary plugin spawn". There is no third route:

1. **`--settings` is argv.** `claudeHookSpawnPrep` prepends `["--settings", <per-pane
   file>]` at spawn (`internal/daemon/daemon.go:4591-4660`), read once at process start.
   There is no IPC into a running claude that accepts a settings path; the same holds
   for codex's `-c hooks=…` (`daemon.go:4475`) and opencode's `OPENCODE_CONFIG_CONTENT`
   (`daemon.go:4536`).
2. **Hooks are snapshotted at startup.** Claude Code's documentation says hook
   configuration is captured when a session starts and later edits do not apply until
   reviewed in `/hooks` *(recalled from the public hooks docs; not verifiable here)*.
3. **The files a running claude might re-read are the wrong granularity.**
   `~/.claude/settings.json` is per user and `.claude/settings.json` per directory;
   Quil's hook is per pane (`QUIL_PANE_ID` in the pane env, `daemon.go:4627`; the hook
   writes `sessions/<paneID>.id`).
4. **Policy forbids touching them.** `internal/claudehook/claudehook.go:15-17`: "This
   package never reads or writes ~/.claude/settings.json — the hook is per-invocation,
   lives entirely under QuilDir, and no user Claude config is mutated."

Consequence for words: **adoption is not attachment.** An adopted pane has had a session
id — read from `~/.claude/projects/…` — recorded in its workspace state. The claude
process in it is unmodified, receives nothing, and does not know Quil exists. The push
events (`internal/claudehook/runhook.go:115-248`) require a process Quil spawned.

## The evidence

The reporter came from the VS Code extension and wanted a persistent wrapper. He created
four tabs with the **Terminal** pane type and typed `claude --resume <session-id>` into
each (one with `--enable-auto-mode`). His daemon log, verbatim:

```
spawn: pane pane-60c5613d cmd=/bin/zsh args=[] cwd=/Users/mike/code restoring=true
spawn: pane pane-c969c743 cmd=.../claude args=[--settings /Users/mike/.quil/sessions/pane-c969c743.settings.json --enable-auto-mode --session-id 831688f0-...] cwd=/Users/mike/code restoring=false
```

The first line is a terminal pane (`daemon.go:5579`; a shell with no args). The second
is a real `claude-code` pane, and it is the only pair under `~/.quil/sessions/` because
`claudeHookSpawnPrep` runs only from the `p.UsesClaudeSessions()` arm of the spawn
switch (`daemon.go:5507-5512`; `internal/plugin/plugin.go:295-300`). A terminal pane's
plugin fails that predicate, so it gets no `--settings`, no `SessionStart` hook and no
`PluginState["session_id"]`. On restore, `resolveSpawnArgs` (`daemon.go:5075`)
dispatches on the *plugin's* strategy — `terminal` is `cwd_only` — and respawns
`/bin/zsh`. Four conversations, gone. #222/#223 fixed the runbook
(`docs/troubleshooting.md:185-220`); this fixes the product.

## Goals

1. Learn, before it runs, that an interactive shell in a terminal pane is about to
   start a session-tracked agent — with exact arguments — on bash, zsh and PowerShell.
2. Start that agent the way `Ctrl+N` would have, in the same pane, with those arguments
   and the session the user named, automatically and within a keystroke's latency.
3. For an agent already running, record its session so a restart resumes it, without
   touching the process.
4. Say so, once, when none of the above is possible.

## Non-goals

- **Attaching to a running process.** See the invariant.
- **A consent prompt at launch.** Nothing is in flight when the function fires
  (§B.9 records the reasoning).
- **Automatic mid-session conversion.** The shell holds state the daemon cannot see
  (§D.2); that path is explicit `Alt+R` only.
- **Rewriting the typed command.** The function hands the line to the daemon or runs
  it exactly as typed via `command`; it never edits it.
- **Writing under `~/.claude/`, `~/.codex/` or opencode's store.**
- **Codex/opencode adoption from disk.** No reader exists (§C.6).
- **Fish.** See Coverage above.

---

## Part A — The seam

### A.1 What the daemon can know today

The scripts emit OSC 7 after every prompt (`bash-init.sh:6`, `zsh-init.sh:11`,
`pwsh-init.ps1:30`), OSC 133 `A`, `B` from preexec (bash and zsh only) and `D;<exit>`
gated on a command having run (`bash-init.sh:8-36`, `zsh-init.sh:14-35`,
`pwsh-init.ps1:9-24`). The daemon parses exactly one: `detectOSC133Exit`
(`daemon.go:3975-3999`) → `command_complete`. OSC 7 is parsed by the **TUI**
(`internal/tui/pane.go:1386`), which sends `MsgUpdatePane`; the daemon stores it in
`setPaneCWD` (`daemon.go:1725-1748`). **None carries the command text.**

### A.2 Where the command line can be learned

**(a) From the shell**, at the moment it decides what to run — bash `$BASH_COMMAND`,
zsh `preexec` `$1`/`$2`; VS Code ships the line as `OSC 633;E` from the same slot
*(recalled)*. Exact, post-quoting, post-alias.

**(b) From claude's output** — banner or title (`applyPluginHandlers`,
`daemon.go:4002-4030`; title stripping, `internal/tui/oscfilter.go:6-16`).
Version-bound, user-disableable, per-agent, and never carries `--resume <id>`.

**(c) From the process table.** `internal/proctree` has the shell's PID
(`session.go:1027`) but `ProcessEntry.Name` is the kernel image name — `comm`
(`table_linux.go:45-57`), `ps … comm=` (`table_darwin.go:59`), Toolhelp32 `ExeFile`
(`table_windows.go:78-81`). An npm-installed `claude` is a `#!/usr/bin/env node` script
whose image name is `node`/`node.exe` on every platform; only the native installer's
binary shows as `claude`. No arguments anywhere. The package states as policy that it
"deliberately performs NO classification. The previous attempt at this feature inferred
process identity from image paths and was wrong on both platforms" (`proctree.go:9-12`),
and the collector is off unless the process dialog is open (`procreport.go:43-47`).

| | Reliability | Arguments | Timing | bash / zsh / pwsh / fish |
|---|---|---|---|---|
| (a) shell | High | Exact | Before exec | yes / yes / yes / no |
| (b) banner | Low, version-bound | None | Seconds after | all |
| (c) proctree | Low (`node`) | None | 5 s tick, gated | all |

(a) is chosen; the others fail on the argument column alone.

### A.3 Where *pre-exec* is reachable

The existing slots learn but cannot stop: bash's `DEBUG` trap skips a command only under
`shopt -s extdebug`, which also makes the trap inherited by every function, subshell and
command substitution *(bash manual)* — breaking the `__quil_arm` gating
(`bash-init.sh:16-19`) and taxing every script line; zsh `preexec` is informational;
pwsh `prompt` runs afterwards (`pwsh-init.ps1:18`); rebinding Enter (readline `bind -x`,
zle `accept-line`, `PSConsoleHostReadLine`) is the most-contended hook in every shell and
sees the raw buffer rather than argv.

What does stop the exec in all three, with parsed argv and no contended hook: **a shell
function with the command's name.** Functions precede `PATH` lookup in bash and zsh, and
a PowerShell function outranks an application (alias → function → cmdlet → application).
Quil's rc is sourced after the user's (`bash-init.sh:3`, `zsh-init.sh:8`,
`pwsh-init.ps1:3`), so the function is installed last and an alias `cl=claude` resolves
to it. It receives `"$@"` — exactly what "reproduce what he asked for" needs.
`command claude …` bypasses it by POSIX definition; that is both the daemon's `run`
answer (§A.5) and the user's escape hatch.

So **(1) pre-exec interception is reachable on bash ≥ 4.1, zsh and pwsh, and carries
arguments.** Bash 3.2 (macOS's `/bin/bash`) lacks `read -N` (§A.5) and is not armed;
macOS's default shell is zsh. Fish is out for want of an init file.

### A.4 Arming a pane: per-shell token, warm pool included

The daemon arms a shell when `hand_started = "convert"` (§Configuration), the pane's
plugin has `ShellIntegration` (`daemon.go:5411-5419`; built-in terminal,
`internal/plugin/builtin.go:19`), the pane is **not sandboxed** and not an overlay. The
sandbox condition is explicit, not structural: `daemon.go:5412` gates shellinit on
`ShellIntegration` alone, `applySandboxSpec` (`sandbox_spawn.go:54-68`) validates the
image and auth mode but not the plugin, and the dialog's hiding of the sandbox row for
`terminal` (`showSandboxField`, `dialog.go:4291-4292`) is presentation — an MCP
`create_pane` with `sandbox_image` on `terminal` reaches `spawnPane` sandboxed. So the
arming predicate is written beside the warm-pool one, which already spells
`!sandboxed` (`daemon.go:5250`), and a test pins both.

Arming adds three variables to the shell's environment:

```
QUIL_INTERCEPT=claude:codex:opencode   # basenames the function must hand over (§A.6)
QUIL_INTERCEPT_TOKEN=<128-bit hex>     # per SHELL — see below
QUIL_INTERCEPT_BASH_MIN=4.1            # documentation only; the script checks BASH_VERSINFO itself
```

**The token is per shell, not per pane, and the reason is the warm pool.** `spawnPane`
claims a pre-spawned shell for exactly the population being armed — `eligible :=
!restoring && typ == "terminal" && !sandboxed && p.Command.ShellIntegration && … &&
!hasInstanceArgs` (`daemon.go:5250-5256`) — and those shells were started in
`warmShellPool.fill` with `s.SetEnv(p.cfg.Env)` (`warmshell.go:117`), an env captured
once, pane-less, by `newShellPoolFor` → `shellinit.Configure` (`warmshell.go:59`).
`WarmShellPoolSize` defaults to 1 (`config.go:600`), so the first terminal pane a user
opens is warm-claimed and every later one as the pool refills. A pane-keyed token cannot
exist at fill time. Therefore:

- `fill` mints a fresh token per shell and stores it on the `warmPoolSession`
  (`SetEnv(append(p.cfg.Env, "QUIL_INTERCEPT_TOKEN="+tok))`); the pool's base env
  carries `QUIL_INTERCEPT` from the registry at daemon start (`newShellPoolFor` already
  has the registry). `TryClaim` (`warmshell.go:183`) returns `(apty.Session, token
  string, bool)`.
- A cold spawn mints its own token at `shellinit.Configure` time.
- `spawnPane` binds the token to the pane — `pane.handStart.token = tok` under
  `PluginMu` — in both branches, replacing any previous value (a restart re-arms).
- The daemon **resolves the pane from the PTY the marker arrived on**: the output
  goroutine already knows `paneID` (`flushPaneOutputGeneration`, `daemon.go:3857`), and
  the marker's token is compared, constant-time, against that pane's bound token. A
  token is never a lookup key.

**What the token is and is not.** It is not proof of the user's keyboard. Any process
running as the user can read it — from `/proc/<pid>/environ`, from the ghost buffer
(0600, same uid, `internal/persist/ghostbuf.go:27`), or from any `MsgPaneOutput` frame
(`read_pane_output` strips it via `ansi.Strip`, `daemon.go:6060`, but the raw broadcast
carries it). A `curl | sh`, an npm postinstall, or a claude Bash tool inside that pane
all hold it. What it does is bind the marker to *that shell in that pane*, so output
arriving from elsewhere — an `ssh` remote printing into the pane, a pasted log, a web
page — cannot trigger it, because that output never had the token. The boundary is
**equivalent to the 0600 socket's, not stronger**: `send_to_pane` already lets any
socket client type into a terminal pane. Stated plainly, a forger with the token gains:
killing that pane's shell and its foreground job, and starting `p.Command.Cmd` (never
the marker's word) with shaped argv (§B.3) in that pane. That is what the user's own
shell can do already.

**A user's own `claude` function wins.** Such wrappers exist precisely to set
environment (§A.5's `FOO=x` class). The script defines Quil's function **only when no
function of that name exists** — `declare -F claude` (bash), `typeset -f claude` (zsh),
`Get-Command claude -CommandType Function -ErrorAction SilentlyContinue` (pwsh). Not an
option: the behaviour. `type claude` then shows whichever is in force; the
troubleshooting doc explains both.

### A.5 The function

For each name in `QUIL_INTERCEPT`, when not already defined:

```
1. skip → run the binary at once when ANY of:
     stdin or stdout is not a tty            [[ -t 0 && -t 1 ]]   /  ![Console]::IsInputRedirected && ![Console]::IsOutputRedirected
     inside a subshell / background job      (( BASH_SUBSHELL > 0 )) / (( ZSH_SUBSHELL > 0 ))   — `claude &`, `$(claude …)`, `( claude )`
     first argument is one of                -p --print -v --version -h --help
2. collect                                   pwd=$PWD; ts=$EPOCHREALTIME (bash ≥ 5 / zsh datetime; else empty)
                                             envnames = names set among CLAUDE_* ANTHROPIC_* CODEX_* OPENAI_* OPENCODE_*
3. stty -echo < /dev/tty                     (restore on every exit path: bash `trap … RETURN` set inside the function; zsh `always`; pwsh `finally`)
4. write the marker to /dev/tty              ESC ] 7770 ; cmd ; <token> ; <name> ; <pwd> ; <ts> ; <envnames> ; <argv joined by \x1f> ESC \
                                             ASCII-only (bytes outside 0x20..0x7E → '?'), ≤ 2048 bytes; over cap → name+token only with '!' and the daemon answers run
5. read exactly 8 bytes from /dev/tty        bash: read -r -N 8 -t 1 reply < /dev/tty      zsh: read -t 1 -k 8 reply < /dev/tty
   with a 1 s deadline                       pwsh: RawUI.ReadKey('NoEcho,IncludeKeyDown') ×8 with KeyAvailable polling
6. restore echo
7. reply == "quil:cnv" → print "quil: starting <name> as a <Plugin> pane"; return 0
   anything else / timeout → command "$name" "$@"          pwsh: $input | & (Get-Command $name -CommandType Application | Select-Object -First 1) @args
```

Design points the review forced, each with the reason:

- **The marker goes to `/dev/tty`, not stdout**, and the function is skipped unless both
  stdin and stdout are terminals. `$(claude -p …)` has a tty stdin and a captured stdout;
  a stdout marker would land in the captured string, the daemon would never see it, and
  the function would stall a second before running. pwsh's `$host.UI.Write` is already
  the console path (`pwsh-init.ps1:22-30`). `-p`/`--print`/`-v`/`-h` are short-circuited
  shell-side rather than round-tripped for a `run`.
- **pwsh pipeline input.** `"text" | claude -p` leaves `[Console]::IsInputRedirected`
  false (pipeline input is objects, not process stdin), so the function fires; `-p` is
  caught by the short-circuit, and every run path forwards `$input` so a pipeline into
  a bare `claude` is not dropped. `Get-Command -CommandType Application` returns an array
  when both `claude.cmd` and `claude.exe` are on `PATH`; `Select-Object -First 1`.
- **Background jobs.** `claude &` in an interactive shell has a tty stdin, so without
  the subshell guard the marker would be emitted and the `read` from the tty in a
  background job would take `SIGTTIN` and stop the job — and a `convert` would then kill
  the shell under a job the user backgrounded. `BASH_SUBSHELL`/`ZSH_SUBSHELL` are
  non-zero for `&`, `$( )` and `( )` alike.
- **Fixed-length reply, no terminator.** The repo has measured that LF is not Enter
  under ConPTY (`task.go:268-273`, `.claude/CLAUDE.md`: "LF does NOT execute in
  PowerShell under ConPTY (measured 2026-09-10)"), so a `\n`-terminated reply read
  "until CR" would never terminate on Windows, time out on every invocation, and answer
  `run` — conversion would silently never fire there. And a `\r`-terminated reply that
  arrives *late* (§A.7) would type `quil:run` + Enter into an agent whose composer the
  `read` had just emptied — a submitted prompt. Reading exactly 8 bytes removes both: no
  terminator to get wrong, and a late reply is at worst eight visible characters in a
  composer, unsubmitted. `read -N` needs bash ≥ 4.1 (the reason for the version gate);
  zsh `read -k 8 -t 1` and pwsh `ReadKey` ×8 are native.
- **Echo off before the marker**, not before the read: echo is applied when bytes
  *arrive*, and `stty` is a fork+exec, so a fast reply written after the marker but
  before `stty -echo` would paint `quil:run` on screen. Order is `stty -echo`, marker,
  read, restore — restore guaranteed by `trap`/`always`/`finally`.
- **Assignments before the call are seen.** `FOO=x claude` with `claude` a function
  sets `FOO` for the function's duration in bash and zsh and exports it to what the
  function runs — the earlier revision stated the opposite and was wrong. The marker
  therefore carries the **names** (never values) of set variables under the agent
  prefixes above; §A.7 decides what to do with them. Values do not travel: the ghost
  buffer would hold them.
- **`$PWD` and a timestamp travel in the marker.** `Pane.CWD` is written by the TUI's
  OSC 7 handler through `MsgUpdatePane` (`setPaneCWD` doc, `daemon.go:1735-1740`), so a
  pane driven by `send_to_pane` with no client attached has a stale CWD; the shell's
  `$PWD` is authoritative and is validated daemon-side like any client path
  (`resolveRequestedCWD`, `daemon.go:2580-2610`: stat, directory, `EvalSymlinks`). The
  timestamp (`$EPOCHREALTIME`, sub-ms, bash ≥ 5 and zsh with `zsh/datetime`; pwsh
  `[DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()`) bounds the late-reply window
  (§A.7); when a shell cannot supply one the daemon uses arrival time with a tighter
  cutoff.

**Privacy.** The tty already echoes the typed line into the PTY output, so the ghost
buffer, every attached client and `read_pane_output` already carry it; the marker
duplicates those bytes in a form `ansi.Strip` removes from excerpts and x/vt never
paints (the ASCII rule exists because x/vt terminates an OSC on `0x9C` inside UTF-8,
`oscfilter.go:6-16`). Logs carry the name, the flag *names* acted on and the env
*names*; never values, never the line, never the token.

### A.6 Daemon side: parser, tracked set, classification

`detectHandStart` joins the two existing detectors at the `flushPaneOutput` call site
(`daemon.go:3927-3929`). It retains an unterminated tail across chunks (≤ 2.2 KB, as
`modeScanTail` does at `daemon.go:3893-3900`) because a split marker must not be missed —
it is answered. Token check first, in constant time, against the pane the output
goroutine is flushing for; mismatch → drop, count, one log line per pane per hour.

**Tracked set derived, not listed.** `handStartTargets(registry)` returns
`map[basename(Command.Cmd)]*PanePlugin` for every *available* plugin the spawn switch
would hook — the three arms at `daemon.go:5483-5512`. `QUIL_INTERCEPT` is its key list.
A `tclaude` plugin with `sessions = "claude"` (`2026-08-19-claude-session-seam-design.md`
§4) is covered with no code change — the "derive the capability" argument
`UsesClaudeSessions`'s doc records (`plugin.go:280-294`). A table test pins the helper
to the switch, and another asserts the intersection of `handStartTargets` with
`ShellIntegration` plugins is empty (the loop guard, §B.8).

**Classification** (`classifyHandStart(plugin, marker)`):

| Shape | Class | Answer |
|---|---|---|
| first positional ∈ claude {`mcp`, `setup-token`, `doctor`, `update`, `install`, `config`, `auth`, `login`, `logout`, `plugin`, `plugins`, `agents`, `migrate-installer`}; codex {`login`, `logout`, `exec`, `apply`, `mcp`, `completion`, `debug`}; opencode {`auth`, `run`, `serve`, `upgrade`} | not a session | `run` |
| `--settings` anywhere (claude) | refused — see S7 note below | `run` + Part E |
| more than one of `--resume`/`-r`, `--session-id`, `--continue`/`-c` | refused: contradictory | `run` + Part E |
| an env name from §A.5 that the **daemon's own** environment lacks | refused: the agent needs this shell's environment | `run` + Part C/E |
| a flag or value failing the §B.3 shape; over cap; unparseable; malformed id | refused | `run` + Part E |
| `--resume <uuid>` / `-r <uuid>` / `--session-id <uuid>`; `resume <uuid>` (codex); `--session <id>` (opencode) | session, explicit id | convert (§B.2) |
| no args, flags only, `--continue`/`-c`, `--resume` without id, a positional prompt | session, no id | convert (§B.2) |

`--settings` is refused rather than merged because `claudeHookSpawnPrep` itself says
what happens when the plugin's args already carry one: it logs "which one claude honours
is unverified, so the hook may not be active" (`daemon.go:4640-4655`), and the card
§B.6 emits says hooks are on. Until the precedence is measured (Live checks), a typed
`--settings` runs as typed. The env-name rule: a name set in the shell **and** in the
daemon's environment is assumed equal (both came from the login environment) and
conversion proceeds; a name the daemon lacks means the respawn would behave differently
— `CLAUDE_CONFIG_DIR` points at a different session store — so the binary runs with the
shell's environment and Part C adopts it. The residual (user re-exported a name the
daemon also has, with a different value) is named on the card by variable name, and
`command claude` is the override.

Subcommands are a *denylist*, so an unknown future subcommand is classed as a session
and converted — the plugin's own `cmd` runs with the user's argv under hooks. The only
misclassification possible is a new *non-interactive* subcommand, which then runs under
the plugin's name; harmless.

### A.7 Answer discipline

Every authenticated marker ends in **exactly one** of: a `quil:run` reply, a restart
(§B.1), or silence because the pane is gone — pinned by a table test with a fake writer.
The reply goes through `Pane.EnqueueInput` (`session.go:416-441`), the ordered per-pane
writer every keystroke uses; the daemon already writes to child stdin off the keystroke
path (`redraw.go:50`, `task.go:276-286`).

**Cutoff.** The shell waits 1 s. The daemon answers only while `now − marker.ts <
700 ms` (or `now − arrival < 400 ms` when the shell sent no timestamp). Past that it
answers nothing: the shell has, or is about to have, run the binary, and eight bytes
typed into an agent's composer is the failure being avoided. The interval is not "~5 ms"
by assertion — the coalescer is 2 ms but `flushPaneOutputGeneration` takes `PluginMu`
first (`daemon.go:3862`), and writer-starvation on that lock is a documented failure
shape (`.claude/CLAUDE.md`, `snapshotWatchdog`). The distribution is a live check and
sets the cutoff.

---

## Part B — Launch-time conversion (primary)

### B.1 What "convert" does, and on which goroutine

Token matched, class "session", session resolved (§B.2), arguments validated (§B.3).
Nothing is in flight: no process, no turn, no composer. Then, **synchronously on the
output goroutine that parsed the marker**:

1. Under `PluginMu`: `Type = <plugin name>`; `InstanceArgs` per §B.3; `CWD` from the
   marker; `ConvertedFromTerminal = true` (§B.8); `disownRecords = true` (§B.4).
2. `retirePaneSessionRecords(pane.ID)` — the five-file loop factored out of
   `cleanupPaneArtifacts` (`daemon.go:2844-2849`: `<id>.id`, `.transcript`,
   `.settings.json`, `opencode-<id>.id`, `codex-<id>.id`), without that function's task
   failing, overlay forgetting and history removal, which belong to destruction.
3. Session claim, per §B.2, which writes `PluginState["session_id"]` + `transcript_path`
   as one unit through `recordResumeSession` (`claudesessions.go:401-413`).
4. `restartPaneInPlace(pane)` — the body of `handleRestartPaneReq`
   (`daemon.go:6425-6480`) factored so the IPC handler and this path share it: swap
   `pane.PTY` out under `PluginMu`, close the old session asynchronously, fail delegated
   tasks, reset the work ledger, `spawnPane(restoring=false)`.
5. `spawnPane` re-reads `pane.Type` under `PluginMu` (`daemon.go:5206-5225`) and takes
   the plugin's arm: `claudeHookSpawnPrep` writes the settings file and logs `claude hooks
   registered` (`daemon.go:4656`); `resolveSpawnArgs` (`daemon.go:5075-5102`) uses
   `InstanceArgs` and appends `--resume <id>` or `--session-id <new>`; codex gets its
   `-c hooks=…` prefix; opencode its env. Same pane id, same tab slot, same layout leaf —
   no `ReplacePaneID`, no client leaf patching (`worktree_client.go:354-372`).
6. One `agent_converted` card (§B.6); snapshot requested.

**Why synchronous.** `detectHandStart` runs inside `flushPaneOutputGeneration` on the
old generation's coalescer goroutine (`daemon.go:3763`, `:3857`). `PluginMu` is released
before the detectors run (`:3925`), so calling `spawnPane` — which takes it — from here
is lock-safe, exactly as `detectBellEvent` re-takes it (`:3950-3960`). What matters is
ordering: `spawnPane` increments `ptyGen` before returning, so every later chunk from
the *old* PTY — the shell's precmd emitting `133;D;0`, `133;A` and OSC 7 the instant the
function returns (`bash-init.sh:22-29`) — fails the generation check at `daemon.go:3863`
and is dropped. A `go`-spawned restart would let that `D` through as a spurious
`command_complete` and that OSC 7 through as a CWD write onto the new pane.

The old shell dies with its PTY. On Unix `unixSession.Close` closes the master **and**
`Kill`s the child (`session_unix.go:68-76`). On Windows `winSession.Close` only calls
`s.cpty.Close()` (`session_windows.go:119-124`) — there is no `Kill`, and whether
closing the pseudoconsole terminates the attached shell is **not settled in-repo**
(Live checks); if it does not, `restartPaneInPlace` must `TerminateProcess` the old PID
on Windows, which the restart path would equally need today.

The cwd is the marker's `$PWD`. The shell's exported environment is not carried — §B.5.

### B.2 Resolving the session at launch

| Typed | Resolution | Outcome |
|---|---|---|
| `claude --resume <uuid>` / `-r <uuid>` | Shape via `resumeSessionIDRe` (`claudesessions.go:39`); `TranscriptPathIn(cwd, id)` must exist; then **`claimResumeSession(pane, [{id, source:"hand-started", transcript, state: located}])`** (`claudesessions.go:376-393`) | `ok` → recorded; the restart's `preassign_id` branch sees `hadSession` (`daemon.go:5311`), the retired hook record reads as absent, and `locatedOwnSession` (`daemon.go:4784-4799`) promotes the located, self-claimed candidate to `--resume`. `!ok` (held by another pane) → **`run`** + card naming the holder. Missing transcript → `run` + card: the session is under another directory's project dir (`EscapeCWD`, `claudesessions.go:75`) or a typo |
| `claude --session-id <uuid>` | Shape check; `PluginState["session_id"] = id`, no transcript path | `locatedOwnSession` finds `candidateUnknown` and declines; `start_args` emit `--session-id <id>` (`claude-code.toml:98`) |
| `claude` bare / flags only | Nothing to resolve; the branch mints a uuid (`daemon.go:5336`) | fresh, with hooks |
| `claude --continue` / `-c` | `claudesessions.List(cwd)` newest-first (`claudesessions.go:214`); newest id not in `claimedClaudeSessionIDs()` (`claudesessions.go:357,455-500`); claim | claude's own rule minus the sibling trap the plugin documents; none free → fresh, logged |
| `claude --resume` (picker, no id) | Refuse → `run` | the user is about to choose; §C.2 recovers it later |
| `claude "prompt"` | Positional passes §B.3 → convert | odd quoting fails the shape → `run`; §C.2 adopts after the first exchange |
| `codex resume <uuid>` | `codexhook.IsValidSessionID` (`codexhook/session.go:47-51`); `InstanceArgs = ["resume", id]` | with `disownRecords` (§B.4) nothing is appended after it |
| `opencode --session <id>` | `opencodehook.IsValidSessionID` (`opencodehook.go:81-88`); pass through | same |
| `codex` / `opencode` bare | fresh | see §B.4 for why opencode does not get `--continue` appended |

**Why not `applyResumeSessionID`.** The earlier revision routed explicit ids through it
and described it as refusing a held session. It does not: it is `func (d *Daemon)
applyResumeSessionID(pane *Pane, raw string)` with no result, and on a held id it logs
"starting a fresh session instead" and returns (`claudesessions.go:53-88`, `:75-78`).
Wired that way, `claude --resume <held>` would have converted into a **fresh**
conversation — the user typed a resume and got a new session, a silent edit of their
argv. `claimResumeSession` returns `(chosen, holder, ok)` and is what the convert path
branches on.

### B.3 Arguments: reproduce the line, under a shape

`InstanceArgs` **replace** `Command.Args` (`daemon.go:5078-5081`), and `.claude/CLAUDE.md`
records why MCP `create_pane` takes toggle *names*: "a free-form list lets any IPC client
run any program under a plugin's name". Two structural differences here:

- **The program is never chosen by the marker.** The daemon spawns `p.Command.Cmd`,
  resolved by `exec.LookPath` at spawn (`daemon.go:5555`); the marker's name is a key
  into `handStartTargets`.
- **The source is bound to the pane's own shell** (§A.4 token) — no stronger than the
  socket, but no weaker, and `send_to_pane` already grants any socket client the same
  power over a terminal pane.

So arguments pass through, minus the session flags §B.2 consumed, under a shape that
keeps argv *inert as argv*: each token matches `^-{1,2}[A-Za-z][A-Za-z0-9-]*(=[^\x00-\x1f]*)?$`
or `^[^\x00-\x1f]+$`, tokens are never re-split, ≤ 64 tokens. Anything else refuses →
`run`. Toggles are *not* consulted: `--enable-auto-mode` arrives as typed, which is what
`args_when_on` would produce (`claude-code.toml:43`).

**Plugin-level `Command.Args`.** A typed line with **no** flags converts with
`InstanceArgs = nil`, so the plugin's own `args` apply — identical to `Ctrl+N` with no
toggle. A typed line **with** flags replaces them — identical to `Ctrl+N` with any
toggle: the dialog sends template/instance args plus toggle args and never merges
`Command.Args` (`dialog.go:2379`, `:2424`, `:5078-5084`), and `resolveSpawnArgs` replaces
(`:5078-5081`). So "the pane `Ctrl+N` would have produced" holds exactly, in both cases;
what the review caught is that the *earlier* sentence claimed more than that. When a
user's `claude-code.toml` carries non-empty `args` and the typed line had flags, the
card names the dropped `Command.Args` so the divergence is visible.

Refusal, not silent editing, is the rule: a daemon that quietly dropped `--model opus`
from a line typed one second ago is the magic the owner does not want.

### B.4 Records, `ownsRecord`, and what `ptyGen` means here

`ownsRecord := pane.ptyGen > 0 || !pane.freshID` (`daemon.go:5381`) encodes "a child of
THIS pane already ran, so it wrote whatever record is under the id" (`:5122-5155`). A
converted pane has `ptyGen > 0` because its **shell** ran, which wrote nothing — so
without correction two things go wrong: (a) for codex/opencode the stale-record retire
at `daemon.go:5404` is skipped and `resolveSpawnArgs` appends `resumeTemplateFor` after
the user's `InstanceArgs` (`:5157-5159`), so a leftover `sessions/codex-<paneID>.id` from
a destroyed pane with a recycled id yields `resume <user-id> … resume <stale-id>`, and a
bare `opencode` gets `--continue` appended (`opencodeResumeTemplate` falls back to
`ResumeArgs`, `daemon.go:4956-4960`; `opencode.toml:45`) — the sibling trap, on a fresh
start; (b) for claude, `--session-id <uuid>` sets `session_id` so `hadSession` is true
(`:5311`), `readHookSessionFn` runs (`:5294-5300`) and a stale hook record **replaces**
the id the user typed (`:5323-5330`). The same class reaches an adopted pane on `Alt+R`,
and `refreshPluginStateFromHooks` (`:498-541`) is ungated, so shutdown could overwrite an
adopted id from a stale file.

Fix, stated as a rule: **`ptyGen > 0` means "an agent child of this pane ran", and
conversion and adoption both break that reading, so both retire the pane's records
first and mark the next spawn as owning nothing.**

- `retirePaneSessionRecords(paneID)` (§B.1 step 2) runs before the claim and before the
  spawn, at conversion and at adoption. After it, `readHookSessionFn`, `readCodexSessionFn`
  and `readOpencodeSessionIDFn` all miss, `refreshPluginStateFromHooks` keeps the
  recorded id, and the first record written under the pane id is the new child's own.
- `pane.disownRecords` (runtime, `PluginMu`) is set by conversion and consumed by the
  next `spawnPane`, which computes `ownsRecord = false` when it is set. That makes the
  session_scrape append at `:5157-5159` not run, so `codex resume <id>` and
  `opencode --session <id>` stand alone and bare codex/opencode start fresh. `ptyGen`
  itself is not reset: it is also the output-generation stamp, and rewinding it would
  let the old shell's frames pass the check at `:3863`.
- The claude `preassign_id` branch is unaffected by `disownRecords`; its guard is
  `hadSession`, and with the record retired the only candidate is the one the claim
  wrote.

### B.5 What is not preserved at launch, and why that is fine

The shell's exported environment, except as §A.6's env-name rule refuses conversion when
an agent-specific name the daemon lacks is set. Beyond that list, a typed AI pane has the
same limitation — `Ctrl+N` panes inherit the daemon's environment, not any shell's — so
conversion produces the pane the supported route would have. Background jobs in that
shell die with it; the subshell guard keeps `claude &` itself out, and a shell with other
jobs that then starts an agent in the foreground is unusual. A job count in the marker
is a recorded option if the live check says otherwise.

### B.6 What the user sees

The command echoes, the pane repaints as the agent starts. One card, `agent_converted`,
severity `info`, group `system`:

```
Title:   Started as Claude Code pane
Message: claude was started by hand in a terminal pane and Quil opened it as a Claude Code pane instead.
         Hooks, session tracking and resume are on. `command claude` runs the plain binary; /exit returns you to a shell.
```

`Data`: `{"agent","session_id","resumed","dropped_plugin_args"}`. The sidebar shows the
title plus the first message line (`internal/tui/notification.go:534-540`); `plainEventGroups`
gains the type explicitly (`notification_class.go:54-67`); `system` defaults on
(`config.go:661`) where `commands` does not (`:662`, `docs/features.md:444`). Through
`emitEvent` (`daemon.go:5661`), so mute applies.

### B.7 Per-agent table

| | claude | codex | opencode |
|---|---|---|---|
| Intercept | yes | yes | yes |
| Explicit session | `--resume`/`-r`/`--session-id` | `resume <uuid>` | `--session <id>` |
| Hooks after conversion | `--settings` + `SessionStart` | `-c hooks=…` with trust hashes; **on the Windows npm `.cmd` shim `codexSpawnPrep` spawns without hooks** (`codexhook.IsShim`, `codexhook.go:281`) — the card says events are off | `OPENCODE_CONFIG_CONTENT` |
| Adopt while running | yes | no reader (§C.6) | no reader |

### B.8 Idempotence, loops, and after exit

A converted pane's child is the agent binary — no shell, no rcfile, no function — so it
cannot emit the marker; the daemon arms only `ShellIntegration` plugins outside
`handStartTargets`, and a test asserts that intersection is empty. There is no state a
loop could run on.

**Clean exit returns a shell.** The review is right that "it stays a dead AI pane" is a
regression for anyone whose loop is shell → `claude` → `/exit` → shell: every cycle
would end in a corpse and a `Ctrl+N`. So `ConvertedFromTerminal` (persisted, like
`Muted`) is honoured by the process-exit path (`daemon.go:3795-3810`): on **exit code 0**
of a converted pane, the daemon re-types it `terminal`, deletes the session keys,
retires the records, clears `InstanceArgs`, and spawns a shell in `Pane.CWD` — a fresh
shell, without the old one's environment, which the card said. A **non-zero** exit
(crash, `Ctrl+C` ×2, login failure) leaves the AI pane and its error screen, where
`Alt+R` resumes. A daemon restart while the converted agent runs restores it as the AI
pane with `--resume`, and the flag survives the restore so a later `/exit` still returns
a shell.

### B.9 Rejected alternatives, with the reasoning

- **Post-exec kill.** Let the function run the binary and the daemon kill-and-respawn
  within ~50 ms. Needs no reply and no wait, which is why it stays as the Windows
  fallback if the pwsh `ReadKey` wait fails its live check. Rejected as primary: a
  started process can have side effects Quil cannot enumerate — whether claude rewrites
  `~/.claude.json` at startup is a live check — and "nothing started" is a stronger
  statement than "nothing had time to happen".
- **Convert only within a launch window** measured by `inputEnqueued`
  (`session.go:433-441`). With the id on disk at any moment the conversation is never at
  risk from a kill; the window protected only in-flight state, which pre-exec has none
  of. The guard survives for Part D.
- **A confirm at launch.** Nothing to consent to; a modal over a pane the user is about
  to type into.
- **Toggle-only argument mapping.** Silently drops any undeclared flag — the failure
  the owner named.
- **Adoption as the default.** Safe and small, but leaves hooks, indicators and history
  on the table at the one moment they are free. Available as `hand_started = "adopt"`.
- **Line-terminated reply.** §A.5: wrong terminator on Windows, submitted prompt when
  late.
- **Overriding a user's `claude` function.** §A.4: such functions set environment.

---

## Part C — Adopt: an agent already running

Reached when the daemon answered `run` — `hand_started = "adopt"`, a §A.6/§B.2/§B.3
refusal the user's command then satisfied, a per-pane "as typed" mark (§Configuration),
or `command claude`. The process is untouched; Quil reads its session id from disk and
records it.

### C.1 What adoption writes, and why the type must change

Under `PluginMu`, in one step, **after `retirePaneSessionRecords`** (§B.4 — an adopted
pane must not read a stale record on `Alt+R` or at shutdown):

| Field | Value | Why |
|---|---|---|
| `Pane.Type` | `"claude-code"` | `resolveSpawnArgs` / `resumeTemplateFor` (`daemon.go:4663-4682`) dispatch on the plugin; `terminal` is `cwd_only`. Re-typing a live pane has a precedent the other way (`spawnRestoredPane` writes `Type = "terminal"` on fallback, hence `PluginMu`-protected, `session.go:153-166`) |
| `session_id`, `transcript_path` | as one unit via `recordResumeSession` | the keys the hook path writes (`daemon.go:531-541`); persisted in `plugin_state` (`daemon.go:4148-4156`) |
| `InstanceArgs` | §B.3-shaped argv minus session flags, when the marker carried one | so the eventual respawn carries the flags |
| `handStart.adopted` | true | latch for §C.4 and the card |

**Restore acts on it:** `claudeResumeTemplate` (`daemon.go:4737-4761`) lists the hook
record, then `PluginState["session_id"]`, then `resume_session_id` (`:4822-4830`); an
adopted pane has a located source-2 candidate; `claimResumeSession` refuses it only if
another pane holds it; template `["--resume", "{session_id}"]`, through the plugin arm
**with hooks**. **Restart acts on it:** `hadSession` true, the record read misses
(retired), `locatedOwnSession` promotes.

### C.2 Resolving the id from disk

**Explicit id in the marker** — claim at once, as §B.2. **Otherwise, scan.** Two in-repo
statements settle *when*: "whose transcript does not exist until the first exchange"
(`daemon.go:4774`) and `claude-code.toml`'s "closed during claude's startup screens …
before any exchange could persist the session". A bare claude is adoptable **after the
first turn**, and there is nothing to adopt before it.

Every 5 s for the first minute, every 30 s after, until `133;D` or adoption:

1. Candidates = regular `.jsonl` files in `ProjectDirIn(ConfigDir(), cwd)` with a
   `resumeSessionIDRe` stem and mtime later than the launch. New
   `claudesessions.ModifiedSince(ctx, cwd, since)` — `List` reads title heads off up to
   `MaxSessions` files (`claudesessions.go:214-311`), the wrong cost for a poll.
2. Drop candidates in `claimedClaudeSessionIDs()`. It **does** include live panes' hook
   records (`claudesessions.go:492-498`) — the earlier "if not, the scan reads them
   itself" branch is deleted — but only for `usesClaudeSessions`-typed panes
   (`:476-478`), so a second hand-started claude in a **terminal** pane beside this one
   is invisible to it. Hence:
3. Drop everything if more than one unadopted launch is pending for this project
   directory, counted **daemon-wide** across all panes' `handStart` state (keyed by
   `EscapeCWD`), not per pane.
4. Adopt iff exactly one candidate remains; claim it. Zero: keep scanning. Two or more:
   refuse, Part E with the reason, stop.

`--continue` under `run`: step 2 drops a session a live pane holds, step 4 never fires,
and the card says the hand-started claude is appending to another pane's session.

**Governing rule:** an id that cannot be resolved with confidence is not adopted.

### C.3 What changes on screen

`wide_canvas` per type (`model.go:3008-3016`, `claude-code.toml:141-144`) — a narrow
split reflows once; the sidebar and `list_panes` report `claude-code` with `agent_state`
empty, documented as unknown (`protocol.go:704-707`); `applyPluginHandlers`
(`daemon.go:4002-4030`) now runs the plugin's error and idle handlers
(`claude-code.toml:119-137`); the redraw key applies under `redraw.go`'s cooldown.
Nothing is spawned, no argv or env changes, no file under `~/.claude` is touched.

### C.4 Reversal when the agent exits

On `133;D` for the adopted launch: `Type = "terminal"`, delete the session keys, retire
records, clear adoption's `InstanceArgs`, clear the latch. Without it, a user who quits
claude and keeps the shell would find the pane respawning `claude --resume` after the
next restart — #221 from the other side. A daemon restart while claude runs has no `D`;
the snapshot holds `claude-code` + `session_id`; the restore resumes.

### C.5 The card

`agent_adopted`, `info`, `system`:

```
Title:   Claude session adopted
Message: This pane now resumes its Claude session after a restart.
         Alt+R upgrades it to a full Claude Code pane (hooks, indicators, history); the shell in it is closed.
```

### C.6 Codex and opencode: claude-only, deliberately

Adoption needs a store indexed by directory. Claude has one (`claudesessions.go:75-92,
214-226`). Codex keeps a date-sharded tree with the cwd inside each rollout, and the
codex spec deferred the reader (`2026-09-04-codex-plugin-design.md`, Follow-ups);
`internal/codexhook/session.go` and `internal/opencodehook` read only Quil's own record
files, and **neither exports a writer** (only `claudehook.WriteSettingsFile` exists,
`claudehook.go:255`), so even the explicit-id follow-up needs new API. Interception
makes the asymmetry small: a codex or opencode started by hand converts at launch.

---

## Part D — Mid-session conversion: the exception

`Alt+R` on an adopted pane; `handleRestartPaneReq` does the work, `locatedOwnSession`
promotes the adopted id, `claudeHookSpawnPrep` attaches hooks. The question is what
must be true at the instant of the kill, and who can know it.

### D.1 What is at risk

The conversation is on disk and `--resume` reattaches to all of it. At risk: **a turn in
progress**; **unsubmitted composer text** (in claude's memory only); **an unflushed
exchange** — whether claude appends per message or buffers is **not settled here**:
`ReadDetail`'s "partial read" note (`claudesessions.go:405-408`) is about context
cancellation, and the readers use a plain `bufio.Reader` with no torn-line tolerance.
Live check.

### D.2 What the daemon can know

| Signal | Verdict |
|---|---|
| `Pane.Work` / `hookevents.WorkLedger` | **Blind** — no hooks; `AgentState` empty is unknown (`protocol.go:704-707`) |
| Transcript mtime advancing | detects a turn that just *ended*; weak |
| PTY output arriving (`LastOutputAt`, `daemon.go:3892`) | claude repaints while idle *(guess)*; uninformative |
| `133;D` | only says there is nothing left to convert |
| `inputEnqueued` since the last `\r` (`session.go:433-441`) | the **one** signal about composer text, as a hint |
| proctree grandchildren | tool execution only; 5 s tick, gated |

Consent carries the weight. The restart confirm (`Restart pane "<name>"?`,
`dialog.go:1869`) gains, for an adopted pane (`PaneInfo.Adopted`):

```
This pane's shell will be closed and claude restarted as a Claude Code pane resuming the same session.
A reply in progress and any unsent text in claude's composer are lost.
```

plus `Keystrokes since your last Enter will be lost.` when `inputEnqueued` advanced
since the last `\r`.

### D.3 Why not automatic here

Adoption made the pane restart-safe, so declining costs nothing that matters; an
automatic kill risks all of §D.1 plus the shell's environment landing on a *running*
conversation — a claude respawned without `CLAUDE_CONFIG_DIR` resumes nothing, with
nothing on screen to say why. No counter-argument found: the launch path already
captured every user who wants automatic.

---

## Part E — Notify

When detection succeeded and nothing above applied: `hand_started = "notify"`; a
codex/opencode `run`; a refusal with a reason (§A.6, §B.2, §B.3, §C.2); or a scan that
has run 10 minutes without a transcript while the command runs (a claude on its trust
screen — minutes, so the card is not made false by an adoption a moment later). For
codex/opencode under `run`, 10 s without `133;D`.

```
Type:     agent_untracked      Severity: warning     Group: system
Title:    Session not tracked
Message:  claude is running in a terminal pane, so this conversation will not resume after a restart.
          Open a Claude Code pane (Ctrl+N) in this directory and pick the session under "Session:" to keep it.
```

The action names the resume picker's label (`renderSetupSessionField`,
`dialog.go:5103-5119`), backed by the same `claudesessions.List`. Codex: `Open a Codex
pane (Ctrl+N) and run 'resume <id>' there`; opencode: `Open an OpenCode pane (Ctrl+N)`.
A refusal with a reason replaces the second line (`session <id8> is open in pane
<name>`; `two new sessions in this directory — cannot tell which is this pane's`;
`--settings was typed; ran as typed — hooks are not active`; `CLAUDE_CONFIG_DIR is set
in this shell; ran as typed`).

Once per pane per launch; repeats across restarts by design. Mute is the persisted
opt-out (`emitEvent`, `daemon.go:5670-5686`; client `model.go:2532-2536`). In the daemon
queue, so `get_notifications` shows it to an agent that typed `claude` via `send_to_pane`.

---

## Configuration

```toml
[agents]
# What Quil does when claude, codex or opencode is started by hand in a terminal pane.
#   "convert" — open it as the matching AI pane instead, with your arguments (default)
#   "adopt"   — run it as typed; track the Claude session so the pane resumes after a restart
#   "notify"  — run it as typed; say that the session is untracked
#   "off"     — run it as typed; say nothing
hand_started = "convert"
```

A new top-level table because none of `config.go:20-30`'s is about what a pane is.
`QUIL_INTERCEPT` is set only under `"convert"`; the other modes never make a shell wait.

**Per pane.** Mute silences `process_exit`, bells and everything else — coarser than this
question deserves — and `command claude` is per invocation. So the context menu gains
**"Run agents as typed in this pane"**, a persisted per-pane mark exactly like `Muted`
(`Pane.AgentsAsTyped`, `UpdatePanePayload.AgentsAsTyped *bool`, `paneData["agents_as_typed"]`).
It cannot undefine a function the shell already loaded, so it works daemon-side: an
armed shell still round-trips (~ms) and the daemon answers `run`; adoption still
applies. That is the whole implementation, and it is why it is in v1 rather than a
follow-up.

## Refusals — what must not happen

- **No attachment to a running process.** Invariant.
- **No argument added to, or removed from, a command the shell runs.** `command "$name"
  "$@"` or nothing.
- **No process killed without the user's own action** (`Alt+R`), except the shell that
  just asked to become an agent, at launch, before the agent exists.
- **No conversion on an unauthenticated marker.** Token first, parse second.
- **No silent editing of argv.** Pass through as typed or refuse; the card names why.
- **No conversion into a fresh session when a resume was typed.** §B.2.
- **No stale session record read by a converted or adopted pane.** §B.4.
- **No claim of a held id, an id whose transcript is not in this cwd's project
  directory, or an ambiguous scan.**
- **No writes under `~/.claude/`, `~/.codex/`, opencode's store, or the cwd.**
- **No arming of a sandboxed pane, an overlay, or a session-tracked plugin's pane.**
- **No token, value or full line in any log.**
- **No interception of `quil mcp`, of `claude setup-token` run by Quil
  (`internal/claudetoken/capture.go:109-115`, its own PTY), or of a claude under a
  `claude-code` pane's Bash tool** — none reaches an armed function.

## Files touched

**`internal/shellinit/scripts/`** — `bash-init.sh`, `zsh-init.sh`: `__quil_intercept`
and the per-name definitions from `QUIL_INTERCEPT` (names validated
`^[A-Za-z0-9._-]+$` before `eval`; skipped when a function exists; bash gated on
`BASH_VERSINFO[0] > 4 || (== 4 && [1] >= 1)`), the tty/subshell/short-circuit guards,
echo-off ordering, `/dev/tty` marker, 8-byte read, `command` run. `pwsh-init.ps1`: the
same with `ReadKey` polling, `$input` forwarding, `Select-Object -First 1`.
`shellinit.go`: `Configure(shell, quilDir, intercept []string, token string)`.
`shellinit_test.go`: script text assertions; `Configure` sets the vars only when armed.

**`internal/daemon/warmshell.go`** — per-shell token minted in `fill`, stored on
`warmPoolSession`; `TryClaim` returns it; pool base env carries `QUIL_INTERCEPT`.

**`internal/daemon/handstart.go`** (new) — arming predicate; `detectHandStart`
(chunk-spanning, token-first); `parseHandStart` + `classifyHandStart` (incl. env-name
rule, `--settings`, contradictory session flags); `handStartTargets`; `Pane.handStart`;
answer discipline + cutoff; `convertAtLaunch`; `retirePaneSessionRecords`;
`adoptClaudeSession`, the scan (daemon-wide pending count), `revertAdoption`;
`returnToShellOnCleanExit`; the three emitters. `handstart_test.go`.

**`internal/daemon/daemon.go`** — `restartPaneInPlace` factored from
`handleRestartPaneReq`; one call in `flushPaneOutput` beside `detectOSC133Exit`; one line
in `detectOSC133Exit`; `spawnPane`: bind the token, consume `disownRecords` into
`ownsRecord`, arming predicate beside the warm-pool one; process-exit path calls
`returnToShellOnCleanExit`; `cleanupPaneArtifacts` calls `retirePaneSessionRecords`.

**`internal/daemon/session.go`** — `Pane.handStart`, `disownRecords` (runtime);
`ConvertedFromTerminal`, `AgentsAsTyped` (persisted; snapshot + parse beside `muted`).

**`internal/claudesessions/claudesessions.go`** — `ModifiedSince`.

**`internal/config/config.go`** — `AgentsConfig{HandStarted}`, default `"convert"`.

**`internal/ipc/protocol.go`** — `PaneInfo.Adopted`, `UpdatePanePayload.AgentsAsTyped`.

**`internal/tui/`** — `notification_class.go`: three types → `groupSystem`;
`dialog.go`: restart-confirm lines (§D.2); `ctxmenu.go`: the per-pane toggle.

**Docs** — `docs/features.md` ("Hand-started agents": coverage list **including that
fish is uncovered**), `docs/troubleshooting.md` (#221 runbook rewritten around
conversion; `command claude`; user-defined functions; fish), `docs/configuration.md`
(`[agents]`), `docs/mcp.md` (three event types), `docs/keybindings.md` (`Alt+R` on
adopted panes; context-menu item), `.claude/rules/hooks-and-sessions.md` (the invariant,
`retirePaneSessionRecords`, `disownRecords`, the `ptyGen` meaning), `.claude/CLAUDE.md`
(one paragraph: function seam, per-shell token, marker, fixed-length reply, fish).
Fragment `changelog.d/added-hand-started-agent-conversion.md`.

## Testing

**Unit, no PTY** (`dev.sh test`):

- `parseHandStart`/`classifyHandStart` table: every §A.6 row; `\x1f` splitting; cap;
  non-ASCII; each session-flag form valid/malformed/missing; `--settings`; two session
  flags; env names present in / absent from a fake daemon env; the §B.3 shape; 65 tokens.
- `detectHandStart` at every chunk-split boundary; wrong token dropped; a marker on a
  pane with no bound token dropped; a marker whose token belongs to *another* pane
  dropped (resolution is by PTY, never by token).
- Warm pool: `fill` mints distinct tokens; `TryClaim` returns the claimed shell's; the
  pane bound at spawn is the one the marker later matches.
- `handStartTargets` vs the spawn switch; the `ShellIntegration` intersection is empty;
  `Available = false` excluded; the arming predicate refuses sandboxed and overlay panes.
- **Answer discipline:** every classification ends in exactly one reply or one restart;
  no reply past the cutoff.
- `convertAtLaunch`: field writes precede the restart; **`claude --resume <held>` →
  `run` + card naming the holder, never a fresh session**; missing transcript → `run`;
  `--continue` picks newest unclaimed; **stale `codex-<id>.id` present → retired, argv
  is `resume <user-id>` alone**; **stale `<id>.id` present with `--session-id` → the
  typed id survives**; bare opencode → no `--continue`; flags present → `InstanceArgs`
  replace, card names dropped `Command.Args`; no flags → nil.
- **The restore pin** through `resolveSpawnArgs`/`resumeTemplateFor`: adopted pane
  (`claude-code`, `session_id`+`transcript_path`, no record) restores to `--resume`; same
  with `restoring=false` promotes; a `terminal`-typed pane with identical state restores
  the shell.
- Return-to-shell: exit 0 on a converted pane → terminal + shell spawn in CWD; exit 130
  → unchanged; the flag survives snapshot/parse.
- Scan rule with fake `ModifiedSince`; daemon-wide pending count with two terminal panes.
- Revert on `D`; `AgentsAsTyped` → `run`; config values; mute → no card; the three
  types classified `system`.

**Live PTY** (dev daemon only — `.claude/rules/dev-environment.md`): see the next section;
every item there is a test step.

## Live checks

Collected here because the code does not settle them; each names what it decides.

1. **Windows shell death on `winSession.Close`** (`session_windows.go:119-124` has no
   `Kill`). Decides whether `restartPaneInPlace` needs `TerminateProcess` on Windows.
2. **pwsh `RawUI.ReadKey`/`KeyAvailable` under the bundled OpenConsole**, on 5.1 and 7,
   with PSReadLine and oh-my-posh loaded: do daemon-written bytes arrive as key events,
   and does the 8-byte read complete? Decides whether Windows takes interception or the
   post-exec fallback (§B.9).
3. **Reply-latency distribution under a loaded daemon** (many panes, a starved
   `PluginMu`). Sets §A.7's cutoff and the shell's deadline.
4. **Whether claude honours the first or last `--settings`** (`daemon.go:4640-4655`
   says unverified). Until measured, §A.6's refusal is the only safe answer.
5. **Transcript flush timing** across a turn (`stat` the file). Feeds §D.1's confirm
   text.
6. **Whether `~/.claude.json` is rewritten at startup.** Feeds §B.9's post-exec risk.
7. **`stty` inside the shells Quil arms** — present on every Unix shell path; and
   confirm `read -N`/`-k` restore the tty mode on timeout.
8. **A daemon stalled > 1 s** (`SIGSTOP` the dev daemon): the function times out, the
   binary runs, and no reply arrives (cutoff). Then a stall just under the cutoff:
   confirm at most eight visible characters land in the composer, unsubmitted.
9. **`ssh` inside a terminal pane; the remote prints a forged marker** → nothing, one
   log line.
10. **The end-to-end #221 shape** on bash, zsh, pwsh: `claude --resume <id>
    --enable-auto-mode` → new `spawn:` line with `--settings … --enable-auto-mode
    --resume <id>`, `claude hooks registered`, spinner on the next turn, `/exit` returns
    a shell. Measure Enter-to-spawn latency.
11. **`claude --version`** → prints, no conversion, no card, no visible delay;
    **`FOO=x claude`** → runs as typed with `FOO`; **`CLAUDE_CONFIG_DIR=… claude`** →
    `run` + adoption card naming the variable.
12. **The x/vt `0x9C` check** with `✳` in an argument.

## Risks / open questions

1. **Coverage is narrower than "typing `claude` converts it"** (Summary): fish, bash 3.2,
   user-defined `claude` functions, agent env vars the daemon lacks, `--settings`, and
   every bypass form run as typed. Each has a card or a documented reason; none is
   silent. The docs must carry the list, not the slogan.
2. **The env-name heuristic** (§A.6) assumes a name present in both shell and daemon
   holds the same value. When it does not, the respawned agent differs from what the
   user configured, and only the card's variable name points at it. `command claude`
   is the override; carrying `CLAUDE_CONFIG_DIR`'s *value* in the marker (a path, not a
   secret) is the one extension worth considering.
3. **Sibling adoption** (§C.2) rests on the daemon-wide pending count; a hand-started
   claude in a shell Quil did not arm (fish, `command claude` on a `notify` daemon) is
   invisible to it. Refusal on ambiguity bounds the damage to a card.
4. **Return-to-shell spawns a fresh shell** (§B.8). Users who expect their old
   environment back will not get it; the card says so.
5. **Which case is most likely to make a user angry?** `Alt+R` on an adopted pane whose
   shell had exported configuration outside the §A.5 prefixes: the respawned claude
   behaves differently and only the confirm's lines warn. Launch-time conversion cannot
   produce it — §A.6 refuses on the agent prefixes, and there is no conversation yet
   for anything else to land on.
