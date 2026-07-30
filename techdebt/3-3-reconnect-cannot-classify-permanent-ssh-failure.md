# Reconnect cannot tell a permanent ssh failure from a transient one

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/tui/reconnect.go` (`reconnectDelay`, `scheduleRedial`), `cmd/quil/remote.go` (`redialRemote`) |
| Found during | Code review of PR #113 (remote-daemon Phase 2 reconnect) — security pass, M2 |
| Date | 2026-07-30 |

## Issue

The reconnect loop retries without limit and without asking *why* the last
attempt failed. Every attempt is a fresh `ssh` with `BatchMode=yes`, so every
attempt is a full authentication.

There is a realistic case — not an attack, a consequence of the design's own
asymmetry — where authentication can never succeed while the link itself is
perfectly healthy:

- The **startup** dial runs non-batch, deliberately, so ssh can prompt for a
  host-key fingerprint or a key passphrase before Bubble Tea takes the terminal.
- Every **reconnect** runs batch, equally deliberately, because by then the TUI
  owns the terminal in raw mode and a prompt would hang the attempt.

So an operator with a passphrase-protected key and no agent authenticates once
interactively at launch, and then every reconnect fails `publickey`
*permanently*. A key in an agent whose socket died behaves the same way, as does
a host key that changed mid-session.

In all three the loop produces a steady stream of failed authentications from the
operator's own address. A default fail2ban `sshd` jail (5 failures / 10 min) bans
them, and the `recidive` jail escalates that to a long ban across every service
on the host — a self-inflicted lockout from a laptop left with the banner up
overnight.

## What has already been done

`reconnectSlowMaxDelay` decays the rate: after `reconnectDecayAfter` attempts the
ceiling rises from 30 s to 5 minutes, taking sustained failure from ~120
authentications an hour to ~33, and the steady-state spacing below the usual
5-per-10-minutes threshold (pinned by
`TestReconnectDelay_SustainedFailureSlowsTheRate`).

**That reduces the risk; it does not remove it.** The early fast attempts — the
ones that make a transient blip heal in under a second — still put roughly ten
attempts into the first ten minutes, which a strict jail can act on.

## Why it is not simply fixed

The information needed is already used on the startup path: `link.ExitCode()` plus
`remoteinstall.ClassifyExit`. But that is not sufficient here, for two reasons:

1. **ssh returns 255 for all of its own failures.** A permanent `Permission
   denied` and a transient `Connection timed out` are both 255. The exit code
   cannot discriminate.
2. **Discriminating therefore means matching ssh's prose**, and this codebase
   deliberately does not do that. `internal/remoteinstall` records the rule
   explicitly: *"Detection is the ssh EXIT CODE, never the string 'command not
   found' (locale-dependent)."* OpenSSH happens not to localise its messages, so
   matching is more defensible here than for a shell builtin — but it is still
   the pattern the project rejected once, on purpose.

## Suggested Solutions

1. **Classify on stderr, narrowly and explicitly.** Match only a short list of
   OpenSSH strings that mean "will never succeed" — `Permission denied`, `Host
   key verification failed`, and BatchMode's prompt-refusal — and park the loop
   with the banner naming the cause plus a key to retry. Document the
   locale-dependence as an accepted exception with the reason (OpenSSH is not
   translated), so the next reader does not think the rule was forgotten.
2. **Park after N consecutive failures regardless of cause**, requiring an
   explicit keypress to resume. No prose matching, and it bounds the worst case
   absolutely — but it breaks the case reconnect exists for: a laptop asleep
   overnight must reconnect unattended.
3. **Ask sshd.** A `ConnectionAttempts=1` probe or reading the exit status of a
   separate `ssh -O check` gives no more signal than the dial already does. Not
   viable.

(1) is recommended, gated on someone confirming the message list against a
current OpenSSH, because getting it wrong in the other direction — treating a
transient failure as permanent — parks a session that would have healed.

## Related

- `techdebt/3-3-batch-ssh-stderr-unbounded-and-unlogged.md` — the same batch-mode
  stderr is the input a classifier would read, and it is currently neither
  bounded nor logged.
- `docs/roadmap/remote-daemon.md` § Work registry (RD-011). PR #113.
