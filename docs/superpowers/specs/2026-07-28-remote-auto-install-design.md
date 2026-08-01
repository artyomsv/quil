# Remote Daemon Auto-Install — Design

> **Status:** design approved 2026-07-28. Implements the "provisioning" gap left
> by [remote-daemon.md](../../roadmap/remote-daemon.md) Phase 1.

## Problem

`quil --remote <host>` requires `quil` to already be installed on the far side,
on the *non-interactive* PATH. Getting there by hand is: download the right
archive for the remote's platform, verify it, copy it over, put it somewhere the
non-interactive shell can see, repeat on every version bump. Two of those steps
have non-obvious failure modes, and one of them (PATH) produces an error message
that looks like a network problem.

Two dead ends exist today:

1. **Not installed** — `ssh host quil --stdio` fails; the TUI reports a
   connection failure and exits.
2. **Version mismatch** — the gate prints `ssh <host> 'quil daemon restart'`,
   which restarts the *same binary* and reports the *same version*. The advice
   cannot work.

## Solution

The laptop provisions the server over the SSH connection it already has.

```
quil remote setup <dest> [--from-dir <path>] [--version <x.y.z>] [--yes]
```

and the same routine, offered inline with a `[y/N]` prompt, when a TUI launch
hits either dead end.

### Why the laptop pushes rather than the server pulling

The alternative — `ssh host 'curl … | sh'` — needs the server to reach GitHub,
which a cluster node often cannot. Pushing works in both cases and guarantees
the installed version matches the TUI by construction rather than by pinning a
variable.

It is also cheaper than it looks: `update.AssetName(version, goos, goarch)`,
`update.FindAssets(rel, goos, goarch)` and `update.extractBinaries(…, goos)`
already take the target platform as a parameter rather than reading
`runtime.GOOS`. Downloading a linux/arm64 archive from a Windows laptop is an
existing supported call.

---

## Platform support

**Local (runs the TUI): all five release platforms.** Local platform is
irrelevant to what is installed — download, checksum and byte-streaming are pure
Go. The only external requirement is an `ssh` binary (built into Windows 10+).

**Remote (runs the daemon):**

| Remote | v1 | Note |
|---|---|---|
| linux/amd64, linux/arm64 | yes | |
| darwin/amd64, darwin/arm64 | yes | |
| windows/amd64 | no | see below |
| freebsd | no | compiles (`internal/pty` build tag) but no release archive; `--from-dir` only |
| linux/arm (32-bit) | no | no release target — refused at probe |

Remote requirements: `sh`, `uname`, `tar -xzf`, and `sha256sum` **or**
`shasum -a 256`. Alpine/musl works: `.goreleaser.yml` sets `CGO_ENABLED=0` for
both binaries, so releases are static.

### Why Windows remote is out of scope

1. `uname` and `sh` are not available — the OpenSSH server's default shell is
   `cmd.exe` unless reconfigured, with entirely different quoting.
2. Windows archives are `.zip` (`format_overrides`), needing a second
   extraction path on the remote side.
3. **A running `.exe` cannot be overwritten.** `mv -f` over a running ELF works
   — the process keeps its inode — which is what makes the Unix install script
   five lines. Windows locks the image file. Quil solves this locally in
   `cmd/quil/update_apply.go` (`freeBackupPath`, rename-aside through `.old`,
   `.old.1`, `.old.2`, because a leftover backup stays undeletable while any
   orphaned process still runs it), but that is Go logic, not a shell one-liner.

Fresh install on Windows would be easy — nothing is running. *Upgrade* is the
hard half, and shipping install-without-upgrade strands the user on second use.

---

## Detection

The signal is the **ssh exit code**, gated on `Established() == false`. If any
byte arrived, quil ran and this is not an install problem.

| Exit | Meaning | Action |
|---|---|---|
| `255` | ssh's own failure (auth, host key, DNS, connect) | existing `reportRemoteLinkFailure` |
| `127` | remote shell: command not found | offer install |
| `126` | found but not executable — wrong architecture | offer re-push |
| other / `-1` | quil ran, or ssh still connecting | existing paths |

Matching on the string `"command not found"` is rejected: locale-dependent, and
on the first dial `Stderr()` is empty by design — that dial is non-batch so ssh
can prompt for a passphrase, which sends stderr to the terminal rather than the
capture buffer. The exit code is available in both modes and every locale.

### Capturing it

`stdioConn` never reaps for status today. Add:

- `exitCode atomic.Int32`, initialised to `-1`
- `reapOnce sync.Once` guarding a `reap()` that calls `cmd.Wait()` and stores
  `ProcessState.ExitCode()`
- `pump()` calls `reap()` after its read loop ends; `Close()` calls it after
  `Kill()`
- `LinkStatus` gains `ExitCode() int` (`-1` = not exited)

Two ordering constraints, both load-bearing:

- **`Kill()` must precede `reap()` in `Close()`.** `sync.Once.Do` blocks the
  second caller until the first returns, so a `Close` racing a `pump` already
  parked in `Wait` would hang until ssh exits on its own. Killing first
  guarantees that `Wait` returns.
- **Read `LinkErr()` before `Close()`, `ExitCode()` after.** `Close` unblocks
  `pump` via `<-done`, which can return without ever setting `pumpErr` — the
  existing comment in `version_gate.go` documents this for `LinkErr`. The exit
  code has the opposite requirement: `Close` is what *guarantees* the child is
  reaped, making a poll unnecessary.

### Loop guard

A binary that will not execute reports `127` from the remote shell — the same
code as "not installed". Without a guard the launch path would install, retry,
see `127`, and offer to install again, forever.

`CGO_ENABLED=0` rules out the musl case; the realistic trigger is `uname -m`
disagreeing with userland (a 64-bit-kernel/32-bit-userland Raspberry Pi OS
reports `aarch64` while the loader is armhf).

After a successful install in this process, a subsequent `127`/`126` must report
*"installed, but will not execute on this host"* and name architecture mismatch
as the cause. It must never re-offer.

---

## Flow

### Probe — one ssh call, script on stdin (`ssh <dest> sh -s`)

Prints exactly five lines and **always exits 0**, so a non-zero ssh exit means a
connection problem rather than a probe finding:

```
1  $HOME
2  uname -s            (or "-")
3  uname -m            (or "-")
4  existing quil path  (or "-")
5  "rw" | "ro" | "-"   (is that path's directory writable)
```

Candidate paths are checked **by absolute path** (`$HOME/.local/bin/quil`,
`/usr/local/bin/quil`) before falling back to `command -v quil`. `command -v`
alone would miss both, for the same non-interactive-PATH reason that motivates
the feature.

Parsing is a pure function over these five lines.

### Fetch — local

`update.FindAssets(rel, goos, goarch)` → download → verify sha256 against the
release's `checksums.txt`. **Nothing is sent before this passes.**

### Push — one ssh call, archive on stdin

The verified archive streams unchanged into an embedded install script, which
re-hashes what it received (catching a truncated stream) and then performs
`install.sh`'s staged-rename install.

**The script cannot travel on stdin — the archive is there.** It is passed as an
argument instead:

```
ssh <dest> "sh -c '<script>' quil-install '<dir>' '<hash>'"
```

ssh joins its command arguments with spaces and applies no quoting, so the whole
string is assembled and escaped locally by a `shellSingleQuote` helper (`'` →
`'\''`). That helper is the single quoting choke point: it also wraps the
persisted binary path used in `RemoteCommand`.

This is why probe and install use different shapes — the probe has no stdin
conflict, so `sh -s` is simpler and avoids quoting entirely.

### Install target

| Case | Target |
|---|---|
| Fresh install | `~/.local/bin` |
| Upgrade, existing path's directory writable | replace in place |
| Upgrade, not writable | `~/.local/bin`, and report that the old copy is now shadowed |

Never `sudo`: a password prompt over a non-tty ssh channel cannot be answered.

### Remember the path

```toml
[remote.hosts."gpu01"]
binary = "/home/artyom/.local/bin/quil"
```

Fed to the existing `SSHOptions.RemoteCommand` on later launches, so the command
is `'/home/artyom/.local/bin/quil' --stdio` and PATH never participates.

A self-healing default command (`sh -c 'command -v quil || exec …'`) was
considered and rejected: it changes the transport for every existing user, and
any shell wrapper is another chance to contaminate stdout, which *is* the IPC
channel.

### Upgrade differs in one respect

The remote daemon is stopped first (`<path>/quil daemon stop`), which kills
in-flight shell commands on the server. Panes respawn from the snapshot;
running commands do not. This gets its own consent line, not a footnote.

Binaries are then replaced by rename — safe while the old process runs, since it
keeps its inode — and the next `quil --stdio` starts the daemon on demand.

---

## Consent

No install ever happens without explicit consent. The prompt names the host, the
resolved target path, the version, and — for upgrades — that the daemon will be
stopped. `--yes` exists for scripted provisioning only.

## Security

- Verify locally before sending; verify again remotely after receiving.
- `validateRemoteDest` applies to **every** new ssh invocation. A destination
  beginning with `-` is parsed by ssh as an option, and `-oProxyCommand=`
  executes a local command (CVE-2017-1000117 class, already guarded once in this
  codebase — each new call site is a fresh chance to reintroduce it).
- Remote stderr keeps the `terminalSanitizer`: these paths print
  remote-controlled text to the operator's terminal.
- No untrusted data is interpolated into shell text. Archive bytes travel on
  stdin; the only interpolated values are locally derived (probe-reported path,
  locally computed hash) and pass through `shellSingleQuote`.
- No `sudo`, no privilege escalation, no system-wide write unless that path is
  already writable by the connecting user.

## Dev builds

`version.IsRelease()` is false for dev builds, so there is no matching release
to fetch — and dev builds are the mandated workflow for this repo
(`.claude/rules/dev-environment.md`). `--from-dir <path>` pushes locally built
binaries through the same remote script: the directory must contain both `quil`
and `quild` built for the remote's platform (`./scripts/dev.sh cross` output).
They are packed into a tar.gz in memory and pushed with a locally computed hash,
so the remote script's verify step is unchanged. This is what makes the feature
testable before release.

`--version <x.y.z>` pins an explicit release.

## Line endings

The install and probe scripts are `go:embed`ed and executed by a POSIX shell.
`.gitattributes` already carries `*.sh text eol=lf`, which covers them by
extension — but this exact class of bug has shipped from this repo before (a
CRLF `bash-init.sh` embedded into a Windows-built daemon broke shell integration
and PATH on every Linux host). A byte-level test asserting the embedded bytes
contain no `\r` is required; the failure is invisible from Windows.

## Out of scope

Windows remotes, sudo / system-wide installs, drift repair (a present but broken
remote install), GitHub mirror configuration.

Drift repair specifically: misjudging a healthy-but-busy daemon as broken would
replace binaries under a live session. It needs its own detection signal, not a
reused one.

## Files

| Path | Change |
|---|---|
| `internal/remoteinstall/probe.go` | Probe output parsing, `uname` → GOOS/GOARCH (pure) |
| `internal/remoteinstall/plan.go` | Target-path decision (pure) |
| `internal/remoteinstall/shellquote.go` | `shellSingleQuote` (pure) |
| `internal/remoteinstall/scripts/remote-probe.sh` | `go:embed` |
| `internal/remoteinstall/scripts/remote-install.sh` | `go:embed` (named distinctly from the repo-root `scripts/install.sh`) |
| `internal/remoteinstall/install.go` | Orchestration |
| `internal/transport/stdioconn.go` | Exit-code capture; `ExitCode()` on `LinkStatus` |
| `cmd/quil/remote_setup.go` | `quil remote setup`, shared prompt, loop guard |
| `cmd/quil/remote.go` | `remoteExitCodeFn` seam |
| `cmd/quil/main.go` | `remote` subcommand; launch-time detection; `RemoteCommand` from config |
| `cmd/quil/version_gate.go` | Remote mismatch arm offers upgrade |
| `internal/config/config.go` | `[remote.hosts.<dest>] binary` |
| `docs/roadmap/remote-daemon.md` | PATH note moves from trap → handled |
| `docs/features.md`, `CHANGELOG.md` | User-facing |

## Testing

Pure and unit-testable: probe parsing, `uname` mapping, target-path decision,
shell quoting, exit-code classification, embedded-script line endings.

`stdioConn.ExitCode()` is tested with a real short-lived child process via the
standard helper-process pattern (portable across Windows and Unix).

End-to-end installation cannot be unit-tested. Manual verification against a
real Ubuntu VM is required before merge, covering: fresh install, upgrade,
`--from-dir`, refusal on unsupported arch, and the loop guard.
