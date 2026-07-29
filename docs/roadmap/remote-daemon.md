# Remote Daemon Attach — `quil --remote`

> **Status: Phase 1 shipped, BETA.** Usable for real work with the limits below.
> Phases 2 and 3 are planned, not built.

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
| **Reconnect is unverified against a real link** | A dropped link now shows a banner and redials with backoff (Phase 2), but every claim about it rests on unit tests against a fake dialer — no manual ssh drop has been exercised yet. Treat it as unproven rather than absent. | Phase 2 manual checks |
| **Filesystem dialogs read the *local* disk** | The pane working-directory picker, git-repository discovery, kube-context discovery and the Claude session list all browse the machine running the TUI, not the one running the panes. Type remote paths instead of browsing. | Phase 3 |
| **Plugin availability is decided locally** | `Ctrl+N` greys out a plugin based on whether the binary exists on *your* machine, not the server's. A tool installed only on the remote is shown unavailable, and vice versa. | Phase 3 |
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

## Phase 2 — reconnect (code complete, unverified on a real link)

A dropped link is a pause rather than an ending.

- The drop is reported as data, not as a quit, so it stays distinguishable from
  the daemon deliberately asking the TUI to exit.
- Redial backs off from 500 ms to a 30 s cap with half jitter, retries without
  limit, and runs in **batch mode** — by then Bubble Tea holds the terminal in
  raw mode, so ssh has nowhere to prompt and a prompt would hang the attempt.
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

## Phase 3 — remote-correct UI (planned)

Make every surface that reads a filesystem read the *server's*.

- Four RPCs: directory listing, git-repository discovery, kube-context
  discovery, Claude session listing — each already a pure function over a path,
  which is why they are movable.
- Plugin registry RPC with server-side `DetectAvailability`, so `Ctrl+N` greys
  out what the *server* lacks.
- `quil status` over the transport rather than refused.
- Update controls targeted at the remote daemon, or explicitly labelled.

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

### Phase 2 — reconnect

Goal: a dropped link becomes a pause, not an ending.

| ID | Item | Blocked by | Status |
|---|---|---|---|
| RD-010 | Distinguish link loss from `MsgCloseTUI` in `listenForMessages` | — | done (code) |
| RD-011 | Redial loop: exponential backoff + jitter, ~30 s cap, unbounded retries, Ctrl+Q aborts | RD-001, RD-010 | done (code) |
| RD-012 | Input freeze + reconnecting banner | RD-010 | done (code) |
| RD-013 | VT, raw ring, scroll offset and selection reset for **every** pane before replay | RD-010 | done (code) |
| RD-014 | Work-state reset or replayed-event dedup (`applyWorkTransition` has no dedup) | RD-010 | done (code) |
| RD-015 | Exactly one live listen loop across the client swap | RD-011 | done (code) |
| RD-016 | `sawFirstState` survives reconnect; ghost re-dim accepted as cosmetic | RD-011 | done (code) |

**"done (code)" is deliberate wording.** All seven are implemented and covered
by 47 unit tests against a fake dialer, with `test`, `test-race` and `vet` green
and the Windows TUI suite passing natively. **None has been exercised against a
real ssh link.** The phase is not closed until the manual checks below pass;
until then the status means "the code is written", not "the behaviour is
confirmed".

Outstanding manual verification:

| Check | Confirms |
|---|---|
| Kill the ssh process mid-session | banner, frozen input, reconnect, and **scrollback not doubled** (RD-013) |
| Sleep the laptop 2 minutes, wake | reconnect with no intervention |
| Shut the remote host down | backoff reaches the 30 s cap, banner keeps counting, Ctrl+Q exits cleanly |
| Drop the link mid-agent-turn with subagents running | spinner reflects reality rather than wedging (RD-014) |
| Local session, daemon stopped | still exits rather than spinning — the local path must be unchanged |

**Decision gate — application-layer liveness: ANSWERED, ssh keepalive.**
`MsgHeartbeat` remains declared in `internal/ipc/protocol.go` and unsent.
Detection rests on ssh's `ServerAliveInterval=15` / `ServerAliveCountMax=3`,
so a silently dead link is noticed in ~45 s. Chosen because the keepalive
already exists, already works through `ProxyJump`, and an app-layer heartbeat
would duplicate it without covering a failure mode ssh misses. Revisit if
Phase 4 removes ssh, or if the manual checks show drops going unnoticed for
materially longer than 45 s.

**Known Windows hazard on this path.** `stdioConn.Close` can block racing the
pump's read on Windows, and `redialRemote` closes the dead client on every
attempt — so a reconnect could in principle wedge with the banner stuck on
attempt 1. Pre-existing (it predates Phase 2 and reproduces on the Phase 1.5
tree), unproven against a real session, and tracked in
`techdebt/3-3-stdioconn-close-races-pump-read-on-windows.md`. Worth watching
for during the manual checks above, since they are the first time this path
runs for real.

### Phase 3 — remote-correct UI

Goal: every surface that reads a filesystem reads the *server's*.

| ID | Item | Blocked by | Status |
|---|---|---|---|
| RD-020 | Directory-listing RPC — the root fix; every other picker keys off the CWD it returns | RD-004 | todo |
| RD-021 | Git repo discovery RPC | RD-004 | todo |
| RD-022 | Kube context discovery RPC | RD-004 | todo |
| RD-023 | Plugin registry RPC with server-side `DetectAvailability` | — | todo |
| RD-024 | Per-target `recent-cwds.json` | RD-020 | todo |
| RD-025 | Empty `AttachPayload.CWD` in remote mode | RD-020 | todo |
| RD-026 | `quil status` over the transport, or documented local-only | — | todo |
| RD-027 | Update controls targeted at the remote daemon, or explicitly labelled | — | todo |
| RD-028 | Async setup-dialog refactor without regressing pinned-height invariants | RD-020 | todo |

**Correction to the limits table above.** The Claude session *listing* is
already remote-correct — `handleClaudeSessionsReq` runs daemon-side and scans
the daemon's disk. What is wrong is the **CWD fed to it**, which comes from
the local directory browser. RD-020 fixes it; no separate session-listing RPC
is needed.

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
| 1 | Does `quil status` gain remote support, or stay documented local-only? | RD-026 |
| 2 | Does the Settings dialog hide daemon-owned rows in remote mode, or show them disabled with an explanation? | RD-027 |
| 3 | Should notes move daemon-side? Storage is already atomic and pane-keyed, so it is a contained follow-up. | unassigned |
| 4 | ~~Application-layer heartbeat, or rely on ssh keepalive?~~ **Answered: ssh keepalive** (~45 s detection). See the Phase 2 decision gate. | RD-011 (closed) |
| 5 | Does Quil issue certificates, or only consume them? | RD-034 |

---

## Related

- [Session Sharing](session-sharing.md) — multi-user, distinct problem
- [architecture.md](../architecture.md) — ADRs
- [features.md](../features.md#remote-daemon-over-ssh) — user-facing description
