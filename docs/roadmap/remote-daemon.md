# Remote Daemon Attach — `quil --remote`

> **Status: Phases 1 and 2 shipped, Phase 3 partly shipped. BETA.** Usable for
> real work with the limits below. Phase 2's reconnect shipped in v1.45.0 and was
> hardened in v1.45.1; it is partially verified against a real link — see the
> manual-check table. Phase 3 has landed every picker that reads a filesystem or
> probes a binary (RD-020…RD-025, RD-028): the working-directory browser, git
> repository discovery, kube contexts, plugin availability and the recent-
> directories list all describe the daemon's machine. `quil status` and the
> update controls are still refused rather than retargeted. Phase 4 is planned,
> not built.

Attach a local Quil TUI to a daemon running on another machine. Panes, tabs and
AI sessions live on the remote host and keep running there when the laptop
sleeps, disconnects, or reboots — the TUI is only a viewer.

```bash
quil --remote gpu01
```

---

## Why this exists

The daemon already survives everything on one machine. The gap is that the
machine doing the work and the machine you are sitting at are increasingly not
the same one: a GPU box, a cluster node, a beefy desktop reached from a laptop.
An AI agent mid-task is exactly the workload you least want tied to a laptop lid.

## How it works

No network port is opened on the remote host. Quil runs:

```
ssh -T <hardening opts> <dest> "quil --stdio"
```

and speaks its normal length-prefixed IPC protocol over that single channel.
`quil --stdio` is the server-side half: it dials the local `~/.quil/quild.sock`,
starting the daemon on demand, and bidirectionally copies. Anything SSH can
reach works — a bastion behind `ProxyJump`, a Tailscale/WireGuard address, a
host on the public internet.

The destination is passed to `ssh` **verbatim**, so `~/.ssh/config` applies
unchanged: `Host` aliases, `ProxyJump`, `ControlMaster`, per-host `IdentityFile`,
hardware tokens (FIDO2/PKCS#11), SSH certificates, `known_hosts`.

### Why the system `ssh` binary rather than a Go SSH library

Shelling out inherits that entire configuration surface for free. Reimplementing
it in-process means reimplementing host-key policy, agent handling, certificate
validation and jump-host chaining — a large amount of security-critical code to
get subtly wrong, for no user-visible gain.

### What Quil forces, and what it deliberately does not

| Forced | Value | Why |
|---|---|---|
| `ForwardAgent`, `ForwardX11`, `ForwardX11Trusted` | `no` | The remote side never needs them, and the daemon protocol spawns processes |
| `PermitLocalCommand` | `no` | Removes a local-execution path from remote config |
| `ClearAllForwardings` | `yes` | Neutralises `LocalForward`/`RemoteForward`/`DynamicForward` in one option |
| `ConnectTimeout` | 15s | An unbounded TCP connect has nowhere to report — the dial runs before the TUI exists |
| `ServerAliveInterval` / `CountMax` | 15s / 3 | The only liveness check; there is no application-layer heartbeat |

Deliberately **not** forced: `StrictHostKeyChecking` (forcing `accept-new` would
be a downgrade, forcing `yes` would break first connections), `ProxyCommand` /
`ProxyJump`, `ControlMaster`, `IdentityFile`. Those are the user's trust and
routing decisions.

OpenSSH takes the *first* obtained value for each parameter and processes
command-line `-o` before any config file, so the forced set genuinely wins.

---

## Security model

The daemon IPC protocol has **no authentication of its own** and never has:
`MsgCreatePane` spawns arbitrary processes and `MsgPaneInput` types into live AI
sessions, so the protocol is RCE-equivalent by design. Security has always been
"the socket is the auth" — `0600`, same UID.

Remote attach does not weaken that. It moves the same trust boundary onto SSH,
which authenticates before the socket is reachable, and opens no listener.

Three caveats worth knowing:

1. **`command="quil --stdio"` in `authorized_keys` is not a confinement.** It
   looks like one, but `MsgCreatePane` grants full shell through the protocol.
   Treat a key that can reach Quil as a key that can reach a shell.
2. **Revoking a key stops new connections, not running ones.** The daemon and
   its panes keep running; stop them explicitly.
3. **Trust now flows server → laptop.** Remote output renders on your terminal.
   Mitigated by Quil re-rendering VT cells rather than forwarding raw escapes,
   OSC 0/1/2 window-title stripping, and the absence of any OSC 52 path — the
   local clipboard is not reachable from remote output. ssh's stderr is the one
   stream that does not go through the pane path (ssh multiplexes the remote
   command's fd 2 onto it), so it is byte-filtered for control sequences before
   reaching the screen.

---

## Phase 1 — shipped (BETA)

- `quil --remote <dest>` attaches over SSH; the remote daemon starts on demand.
- `quil remote setup <dest>` installs or upgrades Quil on the far side. The
  archive is downloaded and checksum-verified on **your** machine and pushed
  over the SSH connection, so the server needs no route to GitHub. Offered
  automatically when a launch finds no `quil` there, or finds the wrong version
  — and the launch then **attaches**, rather than asking you to re-run the
  command you already ran.
- `internal/transport` behind an `ipc.DialFunc` seam, with `Local` (Unix socket)
  and `SSH` backends. The seam is shaped so a TLS backend can be added without
  the protocol layer knowing.
- Bounded failure: a stalled connect gives up in seconds instead of inheriting
  the OS timeout, and an unreachable host is reported as a **connection**
  failure with the exact command to reproduce it — not as a version mismatch.
- Local-daemon lifecycle is guarded, not silently misdirected. `quil restart`,
  `quil daemon start|stop|restart`, `quil status`, the upgrade-restart prompt,
  and `quil --remote <host> mcp` all refuse rather than acting on the wrong
  machine.
- `[remote <host>]` in the status bar, so the machine you are driving is never
  ambiguous.
- Destinations beginning with `-` are rejected (they would be parsed by `ssh` as
  options, and `-oProxyCommand=` executes a local command).

---

## Known limits

These are real and current. None are bugs to be reported; all are scoped work.

| Limit | Effect | Fixed in |
|---|---|---|
| **Reconnect is only partly verified against a real link** | A dropped link shows a banner and redials with backoff (Phase 2, v1.45.0). Two of eight manual checks have been run on a real ssh link; the rest still rest on unit tests against a fake dialer. The highest-value one — reconnecting a pane whose plugin has `ghost_buffer = false` — is outstanding, and is exactly the case the two passing checks could not have caught. | Phase 2 manual checks |
| **Plugin availability is a daemon-lifetime cache** | `Ctrl+N` now greys out what the *server* lacks (RD-023). But the daemon runs `DetectAvailability` at startup and on plugin reload only, and is designed to run for weeks — so a tool installed on the server mid-session stays greyed until the daemon restarts or plugins are reloaded. Local sessions deliberately keep their own detection, which is fresher and describes the same machine. | RD-029 |
| **Plugin *definitions* are still local** | Only availability crosses the wire. A plugin the server defines and the client does not cannot be offered at all, and the F1 → Plugins editor reads and writes the *client's* `PluginsDir()` before telling the remote daemon to reload from its own — so editing plugins over a remote link edits the wrong machine's files. | RD-035 |
| **`quil status` refuses under `--remote`** | It reports on the local daemon, so it is blocked rather than silently wrong. Use `ssh <host> quil status`. | Phase 3 |
| **Update controls are hidden in remote mode** | The update banner describes the *remote* daemon's staged version while every apply path writes to *local* disk. Suppressed rather than offered wrongly. | Phase 3 |
| **Clipboard image paste is local-only** | The DIB→PNG proxy writes the file to the local `~/.quil/paste/` and types a local path into a remote pane, where it does not resolve. | Phase 3 |
| **Notes, log viewer are local by design** | Pane notes and the F1 log viewer read local files. Not planned to change — the daemon logs are reachable over SSH. | — |
| **No multi-client editing** | Two TUIs may attach to one daemon, but the layout is last-writer-wins. | Not planned |

### Provisioning the remote

```bash
quil remote setup gpu01                    # install or upgrade
quil remote setup gpu01 --from-dir ./dist  # push locally built binaries
quil remote setup gpu01 --version 1.43.1   # pin a release
```

Supported **remote** platforms: `linux/amd64`, `linux/arm64`, `darwin/amd64`,
`darwin/arm64`. Any local platform can provision any of them — the download and
verification happen locally, so what the laptop runs is irrelevant to what the
server gets. Requirements on the far side are `sh`, `uname`, `tar` and either
`sha256sum` or `shasum`. Alpine/musl works: releases are built `CGO_ENABLED=0`.

**Windows remotes are not supported.** There is no `uname` or `sh` (the OpenSSH
server's default shell is `cmd.exe`), the archives are `.zip`, and — the actual
blocker — a running `.exe` cannot be overwritten. `mv -f` over a running ELF
works because the process keeps its inode; Windows locks the image file, which
is why the local updater carries the `freeBackupPath` rename-aside logic. A
fresh install would be easy; upgrade is the hard half, and shipping one without
the other strands the user on second use.

Installs go to `~/.local/bin` — **never `sudo`**. An upgrade replaces the
existing binary in place when that directory is already writable, and otherwise
falls back to `~/.local/bin` and tells you the old copy is now shadowed.

### Operational notes

- **`PATH` is handled for you after `remote setup`.** `ssh host quil --stdio`
  runs a non-interactive shell, which on Debian/Ubuntu returns from `~/.bashrc`
  before any `PATH` line, so `~/.local/bin` is usually invisible there. Setup
  records the absolute path per destination (`[remote.hosts.<dest>] binary` in
  `config.toml`) and uses it as the SSH remote command, so `PATH` never
  participates. A **hand-installed** host still hits this — install to
  `/usr/local/bin` or run `quil remote setup` to record the path. Verify with
  `ssh <host> command -v quil`.
- **`ssh <host> quil --stdio` must print nothing.** Its stdout is the IPC
  channel; a shell banner or MOTD on stdout corrupts the first frame. This is
  the single fastest way to isolate a transport problem from a Quil problem.
- **Versions must match** between the local TUI and the remote daemon, same as
  a local session.

---

## Phase 2 — reconnect (shipped v1.45.0, hardened v1.45.1)

A dropped link is a pause rather than an ending.

- The drop is reported as data, not as a quit, so it stays distinguishable from
  the daemon deliberately asking the TUI to exit.
- Redial backs off from 500 ms to a 30 s cap with half jitter and runs in
  **batch mode** — by then Bubble Tea holds the terminal in raw mode, so ssh has
  nowhere to prompt and a prompt would hang the attempt.
- **An attempt counts as restored only once the far side answers.** A dial
  proves the ssh *binary* started, nothing more; against an unreachable host
  every attempt reported success, blanked the panes for a replay that never
  came, and reset the counter so the backoff never engaged. Each attempt now
  completes a version round-trip before the banner clears.
- **Retries stop when retrying cannot help** (v1.45.1). A short list of ssh
  failures that will not fix themselves — rejected key, changed host key, no
  agreeable algorithm — parks the loop instead of retrying; `r` resumes without
  undoing the rate decay. Reachable without an attacker: the first dial is
  non-batch so ssh can prompt for a passphrase, every redial is batch, so a
  passphrase-only key authenticates once and fails `publickey` forever after —
  straight into a default fail2ban jail. Anything unmatched stays transient,
  because mis-parking a session that would have healed is the worse error.
- Input is frozen, not buffered. A keystroke typed at a dead link would arrive
  in a live agent session minutes later at a prompt that has moved on. `Ctrl+Q`
  stays live as the only exit from a host that never returns.
- Every pane's emulator, raw ring, scroll position and the text selection are
  reset before the attach that triggers replay, so reconnecting restores
  scrollback rather than doubling it.
- Work counters are zeroed so replayed `SubagentStart` events cannot wedge the
  spinner. The unseen mark and the user's attention pin survive — they report
  unread work, not a live turn.
- Nothing respawns: the panes never stopped.

Detection rests on ssh's keepalive (~45 s), not an application-layer heartbeat —
see the decision gate in the work registry.

**Both prerequisites were done in Phase 1.5** (RD-001, RD-002):

- The `DialFunc` contract is settled and documented: `ctx` bounds the dial, and
  the returned conn owns the ssh child and releases it on `Close`. The redial
  loop uses `WithTimeout` + `defer cancel()`, which under the old
  `exec.CommandContext` would have killed every session at the moment it
  succeeded.
- ssh's stderr moves to `quil.log` at `tea.NewProgram`, so a late diagnostic no
  longer lands mid-render. It still reaches the terminal during the dial, where
  host-key and passphrase prompts have to be readable — and batch-mode redials
  capture it instead, which is what the reconnect banner shows.

## Phase 3 — remote-correct UI (partly shipped)

Make every surface that reads a filesystem read the *server's*.

**Landed (RD-020, RD-021, RD-028).** Directory listing and git-repository
discovery are RPCs; the Claude session listing was already daemon-side, so three
of the four "RPCs" this phase was scoped around turned out to be two. With them
went `~` expansion, `filepath.Abs`, path joins, filesystem roots, and existence —
each of which had been answering for the wrong machine, and none of which reads
like an RPC until you notice the separator belongs to the disk's platform.

Two consequences worth carrying into the rest of the phase:

- **The trust boundary moved.** Names, paths and error text now arrive from a
  host the user may not control, and are sanitised at render (`sanitizeRemoteText`)
  while raw values stay in state, because the resolved path becomes a spawn CWD
  and a repo path becomes lazygit's `--path`. Any further RPC returning
  daemon-supplied *display* strings inherits this requirement.
- **Every blocking filesystem call now needs a wall-clock bound, not a context.**
  A context cannot interrupt an `os.Stat` or `os.ReadDir` already parked in the
  kernel, which is exactly what an unresponsive mount produces — and each of
  these handlers holds a single-flight slot while it runs. RD-022 walks a
  kubeconfig that may live on such a mount.

**Remaining.**

- `quil status` over the transport rather than refused (RD-026, decided).
- Update controls targeted at the remote daemon (RD-027, decided).
- Re-detect plugin availability on request instead of serving a daemon-lifetime
  cache (RD-029) — blocked on removing `Registry.Get`'s pointer escape first.
- Plugin *definitions* served by the daemon, and an F1 → Plugins editor that
  points at the machine that will load them (RD-035).
- Palette CWD abbreviation against the local home (RD-036, display-only).

**Landed since.** RD-022 (kube contexts, own `kubeDiscovering` slot, names and
namespaces sanitized at render, and scanning/empty/failed rendered apart so an
in-flight scan no longer reads as "no contexts"), RD-023 (plugin availability,
remote-mode only — see RD-029 for why local sessions keep their own detection),
RD-024 (per-host recent list *and* a daemon-side existence check; either alone
leaves the list wrong remotely) and RD-025 (empty `AttachPayload.CWD`).

## Phase 4 — mTLS transport (planned, no date)

The `DialFunc` seam exists for this. A TLS backend with client certificates
removes the SSH dependency for users who want Quil to be its own service, and
is the prerequisite for anything web-facing (M18 #18–19).

---

## Rejected: a relay/broker service

An intermediate service that both ends dial, pairing them by a connection UUID,
so neither needs the other's address.

Rejected because the UUID becomes a bearer token for an RCE-equivalent protocol,
and bearer tokens leak structurally — into shell history, chat, CI logs. It also
means running a 24/7 service (a single point of failure for every user's
sessions), an identity system, bandwidth costs, and liability for proxying other
people's terminals.

Tailscale and similar already provide exactly this property — address-independent
reachability with real identity — and users who want it can have it today with no
Quil-side change, because the destination is passed to `ssh` verbatim. That
verbatim pass-through is also the extension seam: a `ProxyCommand` in
`~/.ssh/config` can route through any broker the user chooses.

---

## Work registry

The canonical list of remaining work. Every item has a permanent `RD-###`
identifier: cite it in commit subjects, techdebt files, branch names and
plan documents.

**IDs are flat and phase is an attribute, not part of the number.** A
positional id (`RD-2.3`) asserts "third item of phase two", which stops being
true the first time work is resequenced — and resequencing is expected here,
because each phase's shape depends on what the previous one settles. Numbers
are never reused and never renumbered; an item that moves phase keeps its id
and changes its row.

Detailed implementation plans live in `docs/superpowers/plans/` (untracked,
one per phase). This table is the durable index; the plans are the working
detail and may be rewritten between attempts.

### Status legend

`todo` · `in progress` · `done` · `blocked` · `dropped`

### Phase 1 — transport and safety (shipped, v1.44.0)

Complete. Recorded for id continuity only.

| ID | Item | Status |
|---|---|---|
| RD-000 | Dialer seam, SSH/Local backends, `quil --stdio`, lifecycle guards, auto-install | done |

### Phase 1.5 — debt that gates the next phases

Small, and two items are hard prerequisites. Do this before Phase 2.

| ID | Item | Kind | Blocks | Status |
|---|---|---|---|---|
| RD-001 | Settle the `DialFunc` context contract — `SSH()` binds the ssh child's lifetime to the *dial* ctx | code | RD-011 | done |
| RD-002 | Divert ssh stderr to `quil.log` once the TUI owns the terminal | code | — | done |
| RD-003 | Correct the stale `runStatus` claim in `.claude/CLAUDE.md` | docs | — | done |
| RD-004 | Plumb `context.Context` into `gitdiscover`, `kubediscover`, `claudesessions` | code | RD-020, RD-021, RD-022 | done |

**Residual after RD-004, carried into RD-020.** The daemon's session handlers
now bound their reads at 10 s, but the TUI's own call sites still pass
`context.Background()` — deliberately, since they run against local disk inside
a synchronous dialog and RD-020 replaces them with RPCs where a deadline is
meaningful. Separately, a `ctx` check between syscalls bounds a scan that is
*making progress*; it cannot interrupt a call already blocked in the kernel on
a dead mount, because Go has no mechanism for that. Read the guarantee as
"bounded work", not "bounded time".

**Why RD-001 is a blocker, not a cleanup.** `transport.SSH()` calls
`exec.CommandContext(ctx, …)`, so cancelling the dial context kills the ssh
child. Inert today only because `dialRemote` passes `context.Background()`.
The natural reconnect code is `ctx, cancel := context.WithTimeout(…)` plus
`defer cancel()` — which would kill each session at the moment it succeeds.
The trap sits exactly where Phase 2's first code goes.

**Why RD-004 was a blocker.** Phase 3 moves directory listing, git discovery
and kube discovery behind RPCs. Those packages did unbounded, uncancellable
filesystem I/O. Locally that is a slow dialog; behind an RPC holding a
single-flight slot it is a stalled scan that rejects every retry while the TUI
reports a timeout. The residual — a scan already parked in a syscall still
wedges the single-flight slot for the daemon's lifetime — is tracked in
`techdebt/3-3-discovery-scan-cannot-be-interrupted-mid-syscall.md`.

### Phase 2 — reconnect (shipped, v1.45.0 + v1.45.1)

Goal: a dropped link becomes a pause, not an ending.

| ID | Item | Blocked by | Status |
|---|---|---|---|
| RD-010 | Distinguish link loss from `MsgCloseTUI` in `listenForMessages` | — | done (code) |
| RD-011 | Redial loop: exponential backoff + jitter, ~30 s cap, unbounded retries (narrowed by RD-019), Ctrl+Q aborts | RD-001, RD-010 | done (code) |
| RD-012 | Input freeze + reconnecting banner | RD-010 | done (code) |
| RD-013 | VT, raw ring, scroll offset and selection reset for **every** pane before replay | RD-010 | done (code) |
| RD-014 | Work-state reset or replayed-event dedup (`applyWorkTransition` has no dedup) | RD-010 | done (code) |
| RD-015 | Exactly one live listen loop across the client swap | RD-011 | done (code) |
| RD-016 | `sawFirstState` survives reconnect; ghost re-dim accepted as cosmetic | RD-011 | done (code) |
| RD-017 | `stdioConn.Close` no longer closes the read handle the pump is parked on (Windows) | — | done |
| RD-018 | Cap the batch-dial stderr buffer; tee it sanitized into `quil.log` | — | done |
| RD-019 | Park the reconnect loop on a permanent ssh failure; `r` resumes | RD-018 | done |

**"done (code)" is deliberate wording.** Every item is implemented, shipped and
covered by unit tests against a fake dialer — 65 test functions in
`internal/tui/reconnect_test.go`, 75 across `internal/transport`, and 4 more in
`cmd/quil/remote_redial_test.go` covering the dial wiring itself — with
`test`, `test-race` and `vet` green and the Windows suites passing natively.
**Two of the eight manual checks below have been exercised against a real ssh
link; six have not.** Shipping did not change what the wording means: for the
outstanding rows the status still reads "the code is written", not "the
behaviour is confirmed", and the phase is not closed until they pass.

RD-017…RD-019 carry a plain `done` rather than `done (code)`. Each was
reproduced first — a 3-in-8 native-Windows hang, an unbounded remote-fed buffer,
a batch dial whose stderr reached no log — and each has a test that fails
against the unfixed code, so the evidence is not "a real link would have shown
it".

Their **wiring** is now pinned too, which the unit tests did not cover: the
transport proves it tees a batch dial's stderr, and the classifier proves it
reads its three signals, but nothing proved `redialRemote` joined either one.
Each of the three new guards was checked by breaking the code and watching it
fail — dialling with a nil sink, building a fresh sink per attempt (which resets
the session byte budget on exactly the flapping link it exists for), and turning
`client.Close()` into `defer client.Close()`. That last one is a one-word edit
that reads as tidier Go, and it silently reverts RD-019: `ExitCode` is only final
once `Close` has reaped the child, so deferring makes every read `-1`, fails the
classifier's 255 gate, and returns every permanent auth failure to an unbounded
retry.

Outstanding manual verification:

| Check | Confirms | Status |
|---|---|---|
| Kill the ssh process mid-session | banner, frozen input, reconnect | **done** — reconnected in 343 ms, 1 attempt |
| Shut the remote host down | backoff climbs, banner persists and names the host unreachable | **done** — 466 ms → 770 ms → 1.288 s, restored on attempt 3 |
| **Reconnect an `opencode` pane and confirm it is NOT blank** | the ghost-replay gate (see below) | **outstanding — highest value** |
| Scroll a reconnected pane to the top | scrollback not doubled (RD-013) | outstanding |
| Sleep the laptop 2 minutes, wake | reconnect with no intervention | outstanding |
| Ctrl+Q during an outage | the only exit from a host that never returns | outstanding |
| Drop the link mid-agent-turn with subagents running | spinner reflects reality rather than wedging (RD-014) | outstanding |
| Local session, daemon stopped | still exits rather than spinning — the local path must be unchanged | outstanding |
| Read `quil.log` after a reconnect and find ssh's own lines in it | RD-018 — the batch arm had no sanitizer and no sink, so diagnostics vanished exactly once a link started flapping. The wiring is now pinned by test; what a real link adds is that the lines are *legible and useful*, which no assertion can judge | outstanding |
| Remove the key from the remote's `authorized_keys`, drop the link | RD-019 — the banner parks and says why, `r` resumes after restoring the key | **classification confirmed** against a live link (2026-07-30): one batch dial with an unauthorized key returned exit 255 and `<user>@<host>: Permission denied (publickey).`, matching the marker list, with nothing established. The banner and `r` still need a terminal |

The two runs recorded above were made on the PR #113 tree, i.e. **before**
RD-017. On Windows that tree could hang on close roughly 3 times in 8, so a
pass there was probabilistic rather than evidence of the fixed behaviour —
re-running both on v1.45.1 is worth the two minutes it costs.

**Verified against the live VM on 2026-07-30, without a terminal.** Both ends
report v1.45.1, and a hand-framed `version_req` sent through
`ssh <host> quil --stdio` came back as a `version_resp` carrying the matching
request id and `"version":"1.45.1"`, with ssh's stderr empty. That is the exact
round-trip `verifyRemoteLink` performs on every reconnect, so the transport, the
remote daemon and the version gate are confirmed end to end — what the remaining
rows need is a terminal, not a working link.

**Phase 3 blocks this check, which is worth stating plainly.** Attempting the run
on 2026-07-30 could not create a lazygit pane at all: the setup dialog's CWD
browser calls `os.ReadDir` in the TUI process, so it offers only the laptop's
drives and can never reach `/home/artyom/homelab` (RD-020); pasting the path with
`ctrl+v` fails too, because `validateAndNormalizeCWD` stats the target locally;
and `Alt+G` reports `no git repo here`, because `overlay.go:54` calls
`gitdiscover.Candidates` in the TUI as well, handing it a Linux CWD to `os.Stat`
on Windows (RD-021). The pane had to be created by sending `create_pane` over the
transport by hand.

**RD-021 is now fixed and confirmed against that same VM**: with the RPC deployed
to both ends, `Alt+G` from the claude-code pane opened lazygit on
`/home/artyom/homelab` directly. The reproduction below is retained because it
is what the fix has to keep being measured against.

RD-021 produced the clearest artifact of the two, because a single screen
contradicted itself: with a claude-code pane open at `/home/artyom/homelab`,
`Alt+G` flashed `no git repo here` in the status bar while the agent running in
that same directory on the VM answered `git status` with `On branch master` and
three modified files. The status bar was describing the laptop's filesystem and
presenting it as a fact about the remote path. `overlay.go:51` already carried a
comment saying the call runs on the local disk and that an RPC would replace it.

So RD-020 and RD-021 are not only UX debt — until they land, the only pane types
that can verify the Phase 2 ghost-replay gate cannot be created through the UI on
a remote host. That raises their priority above the rest of Phase 3.

**claude-code cannot stand in for this check.** It is the obvious substitute —
it is a `ghost_buffer = false` plugin, so it takes the same no-replay path — but
it is the ONLY one that also declares a `redraw_key` (`"\f"`), and `redrawKick`
(`daemon.go`) writes that to the child on every replay-less attach. claude
repaints over whatever is on screen, so a pane that had been wrongly cleared
comes back looking correct. Testing with it produces a **false pass** on exactly
the bug the row exists to catch. The four plugins that expose the gate are the
four it broke — opencode, lazygit, k9s, lazysql — and what they have in common
is `ghost_buffer = false` with **no** `redraw_key`, i.e. nothing repaints them.
Verified 2026-07-30 against a VM that had only claude-code installed.

The TUI half of the contract is nonetheless pinned, and pinned
plugin-agnostically: `TestReconnect_ResetIsConsumedByTheReplayNotPredicted`
drives it at the message level — one pane replayed, one not — so it does not
depend on any plugin's configuration. What a live run still adds is the DAEMON
half: that `handleAttach` really does withhold a replay for those types.

**Why the opencode check is now the most valuable one.** The two checks marked
done both used a *terminal* pane, and code review then found that terminals are
exactly the case that works: `handleAttach` replays only plugins with
`ghost_buffer = true`, so opencode, lazygit, k9s and lazysql got reset with
nothing coming back — a blank rectangle in front of a live process. Fixed by
gating the reset on a replay actually arriving, and covered by a test driven from
the shipped defaults, but not yet seen on a real link. The live run that passed
could not have caught it.

**Decision gate — application-layer liveness: ANSWERED, ssh keepalive.**
`MsgHeartbeat` remains declared in `internal/ipc/protocol.go` and unsent.
Detection rests on ssh's `ServerAliveInterval=15` / `ServerAliveCountMax=3`,
so a silently dead link is noticed in ~45 s. Chosen because the keepalive
already exists, already works through `ProxyJump`, and an app-layer heartbeat
would duplicate it without covering a failure mode ssh misses. Revisit if
Phase 4 removes ssh, or if the manual checks show drops going unnoticed for
materially longer than 45 s.

**Known hazards on this path — all three cleared in v1.45.1.** Recorded here
because each was found by review of Phase 2 rather than by using it, and the
techdebt files they were tracked in have been deleted along with the defects.

| Hazard | Nature | Cleared by |
|---|---|---|
| `stdioConn.Close` blocks racing the pump's uncancellable read on Windows, and a redial closes a client every attempt | pre-existing; Phase 2 turned a per-session event into a per-attempt one. Reproduced natively at 3 hangs in 8 whole-package runs, 0 in 12 after | RD-017 |
| Batch dials buffer ssh stderr with no cap and no logging — Phase 2 made those conns session-length for the first time | ssh multiplexes the *remote* command's fd 2 onto that stream, so the writer is remote-influenced and unbounded. Also silently lost post-reconnect diagnostics from `quil.log` | RD-018 |
| A reconnect cannot tell a permanent auth failure from a transient one, so a passphrase-protected key with no agent retries forever and can trip a fail2ban jail | mitigated by rate decay (~120 → ~33 attempts/hour), not eliminated | RD-019 |

The same property that made RD-018 a hazard also shaped RD-019's fix: because
ssh's stderr carries remote output, the text alone cannot be trusted to say
*ssh* failed — an ordinary `~/.bashrc` printing `permission denied` would
otherwise park the session, and a compromised remote could do it on purpose.
Two independent gates make the text attributable to ssh before it can park
anything: no byte may have arrived on stdout, and the exit status must be ssh's
own 255.

### Phase 3 — remote-correct UI

Goal: every surface that reads a filesystem reads the *server's*.

| ID | Item | Blocked by | Status |
|---|---|---|---|
| RD-020 | Directory-listing RPC — the root fix; every other picker keys off the CWD it returns | RD-004 | **done, confirmed on a real link** (browser reads the daemon; roots, ~, Abs and joins all server-side) |
| RD-021 | Git repo discovery RPC | RD-004 | **done, confirmed on a real link** (Alt+G *and* the setup-dialog pick list, both through `requestGitRepos`) |
| RD-022 | Kube context discovery RPC | RD-004 | **done** (own `kubeDiscovering` slot; contexts sanitized at render; scanning/empty/failed rendered apart) |
| RD-023 | Plugin registry RPC with server-side `DetectAvailability` | — | **done, remote-mode only** (see RD-029 for the staleness residual) |
| RD-024 | Per-target `recent-cwds.json` | RD-020 | **done** (per-host file *and* a daemon-side existence check — either alone leaves the list wrong remotely) |
| RD-025 | Empty `AttachPayload.CWD` in remote mode | RD-020 | **done** |
| RD-026 | `quil status` over the transport, or documented local-only | — | todo |
| RD-027 | Update controls targeted at the remote daemon, or explicitly labelled | — | todo |
| RD-028 | Async setup-dialog refactor without regressing pinned-height invariants | RD-020 | **done** (async browser; height invariant held, pinned-height tests untouched) |
| RD-029 | Re-detect plugin availability on request instead of serving a daemon-lifetime cache | RD-023 | todo — **blocked on removing `Registry.Get`'s pointer escape.** `DetectAvailability` holds the write lock for the whole PATH walk *and* writes `p.Command.Cmd`, while `Get` hands out a raw `*PanePlugin` whose fields every caller reads outside any lock. That race is latent today because detection runs twice in a daemon's life; moving it onto every `Ctrl+N` would make it reachable |
| RD-035 | Serve plugin *definitions* from the daemon, and point the F1 → Plugins editor at the machine that will load them | — | todo |
| RD-036 | `buildPaletteCommands` abbreviates pane CWDs against the *local* `os.UserHomeDir()`, so a server path is shortened against the laptop's home wherever the prefixes coincide | — | todo (display-only) |

**Correction to the limits table above.** The Claude session *listing* is
already remote-correct — `handleClaudeSessionsReq` runs daemon-side and scans
the daemon's disk. What is wrong is the **CWD fed to it**, which comes from
the local directory browser. RD-020 fixes it; no separate session-listing RPC
is needed.

### Cross-cutting — protocol and diagnostics

Not owned by a phase: these describe the IPC layer itself rather than any one
surface built on it.

| ID | Item | Blocked by | Status |
|---|---|---|---|
| RD-038 | Decide whether Quil should ALWAYS set `TERM` for pane children rather than only when the daemon has none (RD-039's fix). Quil *is* the terminal for a pane — it emulates VT via `charmbracelet/x/vt` and re-renders every cell — so the child's `TERM` arguably ought to describe Quil's emulator rather than whichever terminal launched the daemon. Deferred because it changes behaviour for every local user to fix nothing they experience | RD-039 | todo |
| RD-039 | Pane children inherit no `TERM` over a remote link | — | **done, confirmed on a real link** (`ssh -T` allocates no TTY and exports no `TERM`; the daemon started by `quil --stdio` had none and every pane child inherited the gap, so tcell-based tools — k9s, lazysql — exited 1 within milliseconds. Supplied when absent in `internal/pty`; Unix only, since ConPTY children use the Win32 API rather than terminfo) |
| RD-037 | Answer unknown request types instead of dropping them. `handleMessage`'s dispatch switch has no `default:`, so a request the daemon does not understand produces no response and no client-visible signal — the caller waits out its timeout and renders the same empty state a genuine "nothing found" produces. Respond with an error naming the unrecognised type when `msg.ID` is set (a correlated request expecting an answer); broadcasts stay silent | — | todo |

**Why this is worth an id despite being low severity.** The version gate
already covers the case that reaches users: the daemon is long-lived, so a
stale *running* daemon reports its own compiled-in version and the mismatch is
caught at attach. What it cannot catch is two builds that share a version
string and differ in protocol support — in practice an unreleased branch build
against the release.

That is a developer-and-tester hazard rather than a user-facing bug, and it is
recorded because the *diagnostic distance* was the real cost, not the failure.
Hit on 2026-07-31 testing RD-022…RD-025: three unrelated-looking symptoms (no
kube contexts, an empty working-directory browser, k9s starting wrong) all
traced to one daemon that predated the handlers. The information needed already
existed on the wire — the daemon knew it did not recognise `kube_ctx_req` and
said so, in a log on the other machine. One error response turns a cross-machine
investigation into a message on screen.

### Phase 4 — mTLS transport

Goal: remove the ssh dependency for users who want Quil to be its own
service. Prerequisite for anything web-facing.

| ID | Item | Blocked by | Status |
|---|---|---|---|
| RD-030 | TLS `DialFunc` backend with client certificates | — | todo |
| RD-031 | Daemon-side TLS listener behind config, off by default | RD-030 | todo |
| RD-032 | Certificate configuration, trust store, and rotation story | RD-030 | todo |
| RD-033 | Threat-model update — a pre-auth port in front of an RCE-equivalent protocol | RD-031 | todo |
| RD-034 | Decide whether Quil issues certificates or only consumes them | — | todo |

RD-034 is the gate for the whole phase. Issuing certificates makes Quil a CA
responsible for issuance, expiry, storage and revocation. Consuming only
(bring-your-own PKI) is far smaller and is the recommended starting point.

### Recommended order

```
RD-003  ──────────────────────────────►  (independent, do anytime)
RD-001  ──┐
RD-002  ──┼──►  RD-010 ─► RD-012/013/014 ─► RD-011 ─► RD-015/016
          │                                             (Phase 2 ships)
RD-004  ──┴──►  RD-020 ─► RD-021/022/024/025/028
                RD-023, RD-026, RD-027 (independent within Phase 3)
                                              (Phase 3 ships)
                RD-034 ─► RD-030 ─► RD-031 ─► RD-032/033
```

RD-010 comes before RD-011 deliberately: detection is testable against a fake
dialer with no backoff logic present, and getting the `MsgCloseTUI`
distinction wrong makes every later reconnect test ambiguous.

### Open questions

Carried from the design spec; each needs an answer before its phase closes.

| # | Question | Owned by |
|---|---|---|
| 1 | ~~Does `quil status` gain remote support, or stay documented local-only?~~ **Answered: remote support.** The daemon already answers a version handshake, so `MsgStatusReq` is a thin addition. The deciding case is `--json`: a script that gains `--remote` and keeps reporting on the wrong machine is a live failure mode, and refusing prevents it only for as long as the guard is remembered. | RD-026 (decided) |
| 2 | ~~Does the Settings dialog hide daemon-owned rows in remote mode, or show them disabled with an explanation?~~ **Answered: target them at the remote.** Apply drives `quil remote setup <dest>`, which has installed and upgraded over ssh since v1.44.0 — so this is wiring, not new capability. Merely labelling the controls is a strict subset and is the fallback if the phase runs long. | RD-027 (decided) |
| 3 | Should notes move daemon-side? Storage is already atomic and pane-keyed, so it is a contained follow-up. | unassigned |
| 4 | ~~Application-layer heartbeat, or rely on ssh keepalive?~~ **Answered: ssh keepalive** (~45 s detection). See the Phase 2 decision gate. | RD-011 (closed) |
| 5 | Does Quil issue certificates, or only consume them? | RD-034 |

---

## Related

- [Session Sharing](session-sharing.md) — multi-user, distinct problem
- [architecture.md](../architecture.md) — ADRs
- [features.md](../features.md#remote-daemon-over-ssh) — user-facing description
