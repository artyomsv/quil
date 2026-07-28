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
| **No automatic reconnect** | A dropped SSH link ends the session. Panes survive on the server and re-attaching restores them, but it is a manual step. | Phase 2 |
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

## Phase 2 — reconnect (planned)

Make a dropped link a pause rather than an ending.

- Detect the drop, show a reconnecting state instead of exiting.
- Re-dial with backoff, in batch mode (no prompts under raw-mode rendering).
- Re-attach and re-sync workspace state without respawning anything — the panes
  never stopped.
- Decide the `DialFunc` context contract first: `SSH()` currently binds the ssh
  child's lifetime to the *dial* context via `exec.CommandContext`. Harmless
  today with `context.Background()`, but the natural reconnect code
  (`WithTimeout` + `defer cancel()`) would kill a healthy session. Either scope
  `ctx` to the dial or document it as the connection's lifetime.
- Divert ssh's stderr to `quil.log` once the TUI owns the terminal. It stays
  attached for the whole session, so a late ssh diagnostic (`packet_write_wait:
  Broken pipe`) can still land mid-render and corrupt the display. The
  *security* half of this is already fixed — that stream is byte-filtered for
  terminal control sequences before it reaches the screen, since ssh
  multiplexes the remote command's fd 2 onto it — but cosmetically it remains
  an uninvited writer to a terminal the TUI thinks it owns.

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

## Related

- [Session Sharing](session-sharing.md) — multi-user, distinct problem
- [architecture.md](../architecture.md) — ADRs
- [features.md](../features.md#remote-daemon-over-ssh) — user-facing description
