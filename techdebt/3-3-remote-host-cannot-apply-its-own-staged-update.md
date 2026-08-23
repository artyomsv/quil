# A remote host stages updates it can never apply

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `cmd/quil/main.go` — `launchTUI()` (`maybeApplyStagedUpdate`), against the `case "--stdio": runStdio()` arm of the subcommand switch; `internal/update/` staging; `internal/daemon/update.go` |
| Found during | Root-cause investigation behind PR #186 (version-drifted remote offered no upgrade) |
| Date | 2026-08-23 |

## Issue

The daemon checks for releases and STAGES them into `$QUIL_HOME/update/staged/<ver>/`.
Applying a staged release happens in exactly one place: `maybeApplyStagedUpdate(false)`,
called from `launchTUI()`. Nothing ever launches a TUI on a remote host — the client
reaches it as `ssh -T <dest> "quil --stdio"`, and `--stdio` returns from the subcommand
switch well before that call, deliberately.

So a host used only over `--remote` / a `[[destinations]]` entry stages every release
and applies none. Its daemon keeps running whatever version it was last started with,
while the client that talks to it auto-updates on its own schedule. `gateExtraVersion`
refuses any version difference, so the gap closes the destination.

Measured on a real host, 2026-08-23: daemon running 1.55.0 since Aug 14, 1.63.1 staged
since Aug 21, client on 1.63.1. Nine days of `client connected → version_req → client
disconnected` in the remote daemon log with no `attach` between them, while the daemon
itself was healthy and snapshotting `2 tabs, 3 panes` every 30 s.

`quil --version` on such a host is also unusable: it is not a recognised subcommand, so
it falls through to `launchTUI()`, hits the apply prompt, and dies with
`bubbletea: could not open TTY` under a non-interactive ssh session.

## Risks

- Every client auto-update silently widens the gap. The drift is not a one-off state to
  recover from; it re-arms on a timer.
- The only repair is initiated from the CLIENT (`quil remote setup <dest>`, or the
  in-tool offer PR #186 restored). A user who dismisses the offer has no in-tool path
  back until the next launch.
- Staged archives accumulate on the remote — disk spent on binaries that will never run.
- The failure is invisible from the client: `gateExtraVersion` deliberately makes a
  refused destination indistinguishable from an absent one.

## Not addressed by PR #186

That PR fixes the OFFER — the launch-path ask was queued behind a nil provisioner and
dropped, so the drift was silent as well as unrepaired. It does not give the remote any
ability to update itself; it makes the client reliably ask the user to push one.

## Suggested Solutions

1. Let the DAEMON apply its own staged update on a controlled restart, and have the
   client's version gate ask it to. The daemon already owns the stage step and the
   rename-aside swap + rollback (`internal/update/`); what it lacks is a trigger that
   is not a TUI launch. Needs care against `restartDaemonForUpgrade`'s documented
   hazard — a stop that cannot be confirmed must never be followed by a spawn, or the
   host ends up with two daemons owning one workspace.
2. Have `quil --stdio` apply a staged update before connecting, with the prompt
   suppressed (there is no TTY) and consent taken once at `quil remote setup` time.
   Cheaper, but it moves an install into the hot path of every attach, and a failed
   swap would surface as a dial failure rather than an update error.
3. Leave applying to the client and make the DRIFT loud instead: have the daemon stop
   staging on a host that cannot apply, and report `staged but unappliable` in the
   version handshake so the client can say so precisely. Smallest change; does not fix
   the underlying gap.
4. Make `--version` (and `--help`) real subcommands so an operator can read a remote
   host's version over plain ssh without tripping the apply prompt. Independent of the
   above and worth doing regardless.
