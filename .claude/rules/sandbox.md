---
description: Docker sandbox panes — the mount set as a security boundary, the per-pane object store and its ordered teardown, the sign-in flows, and refusals that never soften.
paths:
  - internal/sandbox/**
  - internal/daemon/sandbox*.go
  - internal/tui/sandbox*.go
  - internal/claudetoken/**
  - internal/userenv/**
  - cmd/quil/sandbox*.go
  - docker/sandbox/**
  - scripts/sandbox-image.sh
---

# Docker sandbox panes

Design doc: `docs/superpowers/specs/2026-09-07-docker-sandbox-design.md`. It
records four revisions, each after a review found a fatal flaw in the previous
one — a cross-pane escape, a mount set that could not `git add`, and a `/repo`
mount that exposed the user's whole main checkout. Read its "What the earlier
versions got wrong" section before changing the mount set.

## The mount set is the security boundary

`internal/sandbox` is pure path arithmetic except `docker.go`, deliberately:
`dev.sh test` runs the suite inside a container with no Docker, and a boundary
that needs a daemon to test is a boundary nobody tests. Every rule below is
pinned by a table test.

**Only the repository's `.git` enters the container, never the main checkout's
working tree.** Mounting the checkout root was measured handing the agent
`cat /repo/.env` → `SECRET=hunter2`. It also removes the nested-worktree
exposure by construction.

**`/repo/.git/objects` is READ-ONLY.** New objects go to the pane's own store
via `GIT_OBJECT_DIRECTORY`, with the repository's store as a read-only
alternate. Objects are every commit for every branch, so a writable store makes
one `rm -rf` an unrecoverable loss of the repository's local history rather
than the sandboxed worktree's.

**Exactly four things are writable, and each has a reason.** `refs`, `logs` and
`worktrees/<name>` because a linked worktree keeps its index and HEAD in the
MAIN repository — without them `git add` answers
`Unable to create '.../index.lock': Read-only file system`. Everything else in
`.git` stays read-only because **host git EXECUTES `.git/hooks` and honours
`.git/config`**: a container that can write them has host code execution,
measured via `core.fsmonitor`.

**Both `objects/info` directories are shadowed with an empty read-only mount.**
Git follows `info/alternates` and `info/commit-graph` transitively. The `/repo`
side carries the line Quil itself writes in host path form, which container git
cannot normalise — it printed `error: unable to normalize alternate object
path` on EVERY git invocation inside the container. The `/quil` side is
agent-writable and is what the host's own line points at.

**The two gitdir overlays live outside every mounted directory.** A read-only
flag is per mount point, so an overlay reachable read-write elsewhere is
rewritable by the agent — and rewriting `admin-gitdir` lets `git worktree
prune` inside the container delete the host's worktree registration, taking
uncommitted work with it. v2 put them inside the pane's own root and the agent
rewrote them through the other path.

**Container-side paths are `/`-joined, never `filepath.Join`.** On a Windows
daemon that emits backslashes. `RunArgs` takes `hostGOOS` as a parameter rather
than reading `runtime.GOOS` so both shapes are testable on Linux CI.

## The object store, and the one ordering rule

Teardown is: **harvest → kill the container → remove the alternates line →
remove the per-pane tree → remove the worktree.** Every step depends on the one
before. Harvesting after the line is gone loses the commits; deleting the tree
while the container lives lets the container recreate it; removing the worktree
while the container holds the bind mount fails, with three retries at 250 ms.

**The harvest copies only `??/` shards and `pack/pack-*`, never `info/`.** A
plain copy of the store was measured dragging an agent-written `commit-graph`
into the user's real object store after a container-side `git repack -a -d`.
Note `git gc` FAILS in the container (no `gc.pid.lock` on the read-only mount)
but `git repack -a -d` SUCCEEDS, so packs are a real case.

**The alternates line is in the host's NATIVE path form.** An MSYS-style
`/c/...` makes git answer `unable to normalize alternate object path` on every
command in the repository.

**Removal deletes only this pane's line.** Two sandbox panes on one repository
write two lines, and the user may have one of their own.

A dangling line makes EVERY git command in that repository fail. The registry
at `$QUIL_HOME/sandbox/alternates.json` records which repository each pane
wrote into, so the startup repair knows where to look. It is best-effort: the
registry lives in `$QUIL_HOME` too, so `reset-daemon` defeats it, and the docs
carry the one-line manual fix.

**`teardownSandbox` RETURNS whether the container is gone, and the worktree
removal is gated on that bool** — the comment claiming the ordering was not
enough, because the function returns early on a failed `docker rm` and the
caller went on regardless. A live container holds the worktree as a bind mount
and `removeOwnedWorktrees` FORCES the removal after three attempts at 250 ms,
so it does not simply fail: it deletes the directory out from under a running
agent. Both close paths gate on it, and the tab path cancels for the whole tab
when ANY of its panes' containers survived, since the worktrees are shared
across them.

**The no-registry-entry branch returns TRUE even when its removal errors, and
that asymmetry is load-bearing.** The registry entry is what says a container
ever existed; with none, nothing holds the worktree. On a machine with no
docker installed — most of them, and every one of them closes worktree panes —
that call fails with `executable file not found` for EVERY ordinary pane, so
gating on it would quietly stop removing worktrees for users who never opted
into a sandbox at all. `TestTeardownSandbox_ReportsWhetherTheContainerIsGone`
carries that case explicitly.

## Refusals that must never soften

A sandbox pane **never falls back to a host spawn**. An unavailable engine and
an unsafe mapping both fail the spawn with `SpawnError`. A pane the user asked
to isolate must not quietly run unisolated.

`NewMapping` refuses when `$QUIL_HOME` lies inside a path it would mount — the
dev build puts it inside the checkout, so sandboxing a quil worktree would
mount every pane's settings file and, on Linux, the daemon socket. Containment
is tested on **symlink-resolved** paths: a junction defeats a string prefix
test.

**`.git/modules` is found with a `stat`, and a stat FOLLOWS links** — so it
needs its own containment check and does not get one from the two candidates
above. It is a bind SOURCE in its own right, so a symlink or junction there
makes an unrelated host directory appear at `/repo/.git/modules` inside the
container. Read-only, but read-only host data the boundary exists to exclude.
`refuse` resolves it and demands it stay inside the resolved `.git`; git never
creates such a link, so the refusal costs no real repository anything.
`TestNewMapping_RefusesGitModulesLinkedOutsideTheRepository` drives it through
the `evalSymlinksFn` seam, with an accepting twin so the refusal cannot degrade
into "submodule repositories never map".

The persisted pane type carries a `sandbox/` prefix. Auto-update has a rollback
path, and a daemon too old to read `sandbox_image` would otherwise restore the
pane as an ordinary one pointed at the worktree — an agent on the host, with no
error anywhere. An unknown type takes the existing fallback to `terminal`.

A **claude-code** pane refuses to spawn without a hook binary. Running hookless
is not a degraded mode there: with no `.id` record the next restart passes
`--session-id` for an id whose transcript exists, which claude refuses with
exit 129.

## Everything that reads agent-written content goes through `os.Root`

The pane's tree is written by a process inside the container. On a Linux host —
including the remote-daemon case — a symlink planted there resolves on the
HOST. Measured: `ln -s /etc/passwd` inside the container succeeded on the bind
mount and a host-side reader printed the host's own file. **On Windows the same
link is an inert reparse point, so a Windows-only test never sees this.**

The harvest and the spool forwarder both open through `os.Root`. Any new
daemon-side read under `$QUIL_HOME/sandbox/panes/<id>` owes the same.

## Hooks, and the three GOOS reads

`hookPaths` separates "where to WRITE this file" from "what to CALL it". Same
directory for a host pane, two different ones for a sandbox pane — conflating
them hands the container a `--settings C:\Users\...` it cannot open.

Codex takes the child's OS explicitly and it reaches **three** independent
decisions: `HookCommandFor` (the shell spelling, which is hashed into the trust
key), `ConfigOverrideArgs`'s GOOS argument, and the shim check. Switching fewer
than all three leaves codex prompting for trust on every pane.

The spool forwarder respects four `Spool` invariants a naive copy breaks: it
opens and closes per batch (a long-lived handle strands the file on Windows),
caps bytes per pass, never replays from offset zero on restart, and recovers
from truncation.

## Scoping and labels

Every container carries `quil.pane` AND `quil.home`. Docker labels are
engine-wide and the dev and production daemons share one engine, so a sweep on
`quil.pane` alone would have a dev daemon force-remove every production sandbox
container — the always-on `dev-environment.md` rule. `quil.home` is
canonicalised before hashing, because `QuilDir()` returns `$QUIL_HOME`
verbatim.

The sweep command is `docker ps -a --filter … --format …`, **without `-q`**:
`docker ps -aq --format` prints `WARNING: Ignoring custom format` and returns
container ids.

## Shutdown

`docker kill` in parallel under a 2 s budget, never `docker stop`. Stop's grace
is ten seconds per container and claude as PID 1 ignores SIGTERM, while the
daemon's whole budget is five seconds before it is SIGKILLed. Killing the
docker CLI does **not** stop its container, so leaving them running would leave
agents nothing can reach or stop.

## Still unmeasured

Two items block a confident ship and are marked in the design doc: resize
propagation through `docker run -it` under ConPTY (the one area of this codebase
with documented irreversible damage), and the default in-container sign-in.

## The image is BUILT, never pulled

`docker/sandbox/Dockerfile` + `scripts/sandbox-image.sh` produce
`quil-sandbox:latest` on the user's own machine. Nothing is published, nothing
is pulled but the base image, and CI does not build it.

**Never add a default that names a registry image.** `anthropics/claude-code`
on Docker Hub is a security researcher's HONEYPOT — it is not Anthropic's, runs
as root, and contains no Claude Code; it exists to study tools that assume an
official-looking namespace is official. Docker Hub namespaces are first-come
and unrelated to the GitHub org. Anthropic publishes no pullable image at all;
their guidance is a dev-container recipe you copy.

The build script VERIFIES by asking the image — non-root user, working
`claude`, `git` — rather than trusting a clean build log. The recipe it
replaces was hand-copied into the docs WITHOUT its install step, producing an
image with no `claude` on PATH that failed at spawn with the error the same doc
page described three paragraphs later. `--check --tag <tag>` runs the assertions
against a user's own image.

`pwd -W` in that script is load-bearing on Windows, exactly as in `dev.sh`:
under Git Bash a plain `pwd` yields `/e/...`, which Docker Desktop — a native
Windows process — cannot resolve, and the build fails with "unable to prepare
context".

Sandbox panes have **no network egress restriction**. `RunArgs` passes no
`--network` and no `--cap-add`, so the iptables firewall in Anthropic's example
dev container cannot work here (it needs `NET_ADMIN`). The mount set is the
boundary this feature enforces; egress is not, and the docs say so.

## The BROWSER flow is the default, and only `"token"` selects the other one

**The token flow is not contained by the pane, and that is why it cannot be the
default.** `claude setup-token` mints a credential that `internal/userenv`
saves to the user's persistent environment — Quil deliberately keeps no copy of
its own — and Windows builds every new process's environment from its parent's,
so the daemon and therefore every ORDINARY Claude pane it spawns inherits it.
Claude Code prefers that token over an interactive login. Measured 2026-09-21:
a user who had opened one sandbox pane found every pane, sandbox or not,
authenticating as "Claude API" with a smaller `/model` list, and their usage off
the subscription they were paying for. Nothing on screen connected the two, and
the diagnosis took a registry write-time comparison against two daemons' start
times. A default that can move someone's billing is wrong whatever the sign-in
cost of the alternative.

**Read `Auth` only through `SandboxConfig.ResolveAuth`.** `""` resolves to the
BROWSER flow, and that is the migration rather than a nicety: `Load` starts from
`Default()` and lets the decoder overwrite only the keys a file NAMES, and
every `config.toml` on disk names `auth = ""` — so changing `Default()` alone
reaches no existing install (the property `unfocused_dim_enabled` documents).
Making the zero value mean the intended default is the only change that reaches
everyone.

**The resolver is asymmetric on purpose**: only the exact string `"token"`
selects the token flow. `""`, a typo, the wrong case and stray whitespace all
resolve to browser, which stores nothing and therefore cannot move anyone's
usage anywhere. `TestResolveAuth_OnlyAnExplicitChoiceSelectsTheToken` pins that
as a property rather than as a list of literals, because the defect class here
is a default drifting, not a spelling.

It meant browser once before, was changed to token because `""` was then
indistinguishable from "unset" (nothing could ASK for the fallback, so every
pane re-prompted with nothing explaining why), and is browser again for the
reason above. That earlier argument no longer holds: `"browser"` is a value a
config can name.

**A test about token behaviour must NAME the mode** — `tokenFlowConfig()` in
`internal/daemon/sandbox_signin_test.go`. Nine tests there described "the
default" while their names claimed to describe the token flow, and all nine
went red on this change rather than one.

`sandboxIdentity` and `dockerCLIEnv` must agree, and both now take the RESOLVED
mode. They used to compare against the literal `"token"` separately, so a
change to the accepted spellings had to land in both or docker is handed a
variable NAME the identity decided not to forward — or a value nothing asks
for. `TestSandboxAuth_IdentityAndCLIEnvAgree` pins the pair.

**The token flow with no token in the environment logs the remedy.** That state
is indistinguishable from a broken feature at the pane: claude simply asks the
user to sign in. The daemon's own environment is the one place a user would not
think to look, so the message names the command and the alternative.

An unrecognised `auth` value resolves to the fallback and is reported, rather
than refusing: it is a typo in a sign-in preference, not an isolation property,
and the browser path uses no credential at all. This is NOT an exception to
"refusals that must never soften" — that rule is about isolation.

## `quil sandbox login` drives the setup; the OS holds the token

`cmd/quil/sandbox.go` + `internal/userenv`. The command runs Anthropic's own
`claude setup-token`, takes the pasted token, writes it to the OS user
environment, and restarts the daemon.

**Quil must never keep its own copy.** `claude setup-token` mints a Claude
session token, and storing one reads as "collect, store, or intermediate …
session tokens" — the same wording that parked copying
`~/.claude/.credentials.json`. Writing it to `HKCU\Environment` makes Quil a UI
over somewhere the user could have typed it themselves. A Quil-owned encrypted
store was considered and REJECTED for that reason; do not add one.

**Both the process env and the persistent store are written, and both are
needed.** Windows builds a child's environment from its PARENT's, not from the
registry, so a registry-only write would not reach the daemon this same run
restarts — and a process-only write would not survive a reboot.

**`setx` is not used**: it truncates at 1024 characters, and a truncated token
fails authentication with nothing indicating anything was cut.

The token is CAPTURED from the command's output under a PTY, not pasted. A plain
pipe cannot do it — MEASURED: with stdout redirected `claude setup-token`
prints nothing at all, opens the browser, and waits. The PTY is 400 columns
wide on purpose: a pseudo-terminal HARD-WRAPS at its own width, inserting a
real newline a scanner cannot tell from an intentional one, and the width is
ours to choose. Output is mirrored verbatim so the browser step stays visible
and interactive; only the scan copy is ANSI-stripped, and it runs over the
ACCUMULATED stream because a read boundary can split the token.

**Accumulating is only half of it: the match is accepted only once the stream
proves where the token ENDS** (`completeToken`). `{20,}` is a floor, not the
token's length, so a read that ends 25 characters into a 43-character token
leaves a match that satisfies the pattern and is a PREFIX of the credential —
which is then persisted to the user environment and forwarded to every
container, where it fails to authenticate with nothing on screen saying why. A
truncated credential is worse than a late one. A match is therefore taken only
when a byte the token could not contain follows it, or when the stream has
ENDED — that second case is required, since a stream can legitimately end on
the token's last byte. `TestCapture_DoesNotReturnAPrefixOfASplitToken` drives a
real two-write split through `Capture`, because the decision lives in the
reader goroutine.

**Unix `login` must not claim success it did not achieve.** Persistence is
unsupported there, so the token lives only in the exiting process; the closing
line says so and names what to do, instead of "sandbox panes will use this
token". Same rule one level down: the daemon restart is only attempted when
something was persisted AND a daemon is running, and the message distinguishes
"this daemon has it" from "the next one will".

Unix returns `ErrUnsupported` rather than guessing a shell profile: which file
depends on the shell and the session, and a daemon under launchd or systemd
reads none of them — so a write that "succeeded" would leave the daemon exactly
as unable to see the variable, while reporting success. The command prints the
export line instead.

`internal/userenv/userenv_windows_test.go` is behind `//go:build windows`, so
CI never compiles it. Run it natively — `GOOS=windows go test -c` in Docker,
run the `.exe` on the host — and note the round trip takes ~0.8 s because
`broadcastEnvChange` waits out its `SendMessageTimeoutW` budget.

## `empty-file` must be ENSURED, not re-created

`ensureEmptyShadowFile` stats first. The version it replaced opened with
`O_CREATE|O_WRONLY` and forgave `os.IsExist` — but `O_CREATE` WITHOUT `O_EXCL`
never returns EEXIST, it opens the existing file for writing, and this file is
created `0400`. On Windows that sets the ReadOnly ATTRIBUTE, so the open
answers "Access is denied", which `os.IsExist` does not match. **The first
sandbox pane on a fresh `QUIL_HOME` worked and every one after it failed to
spawn.** The forgiving branch was checking for an error the call could not
produce.

**A Linux test cannot catch this.** `dev.sh test` runs as ROOT, where mode
`0400` is not enforced, so the idempotency test passes against the broken
implementation — verified by mutation. The real coverage is
`sandbox_shadow_windows_test.go`, behind `//go:build windows`: build it with
`GOOS=windows go test -c` and run the `.exe` on the host. It asserts the
ReadOnly attribute as a PRECONDITION so it cannot pass vacuously.

The host mode is not the boundary — the mount carries docker's own `readonly` —
so the file only has to EXIST and be EMPTY. A directory there is refused
(docker invents one for a missing bind source) and so is a non-empty file: it
is mounted over `config.worktree`, which host git executes values from.

## A failed spawn must reach the PANE

`constructPaneAt` writes `pane.SpawnError` before returning. Every caller used
to log the error and move on, so a pane that failed to spawn was published,
broadcast and drawn as an EMPTY BLACK RECTANGLE with the reason only in
`quild.log`. Sandbox refusals are what make that unacceptable: they exist so
the user is told rather than silently handed an un-isolated pane, and a black
pane tells them nothing. `spawnPane` clears the field on a later success, and
it is never persisted — a stale error surviving a restart would describe a
condition that may be long gone.

## The pane signs itself in

`internal/daemon/sandbox_signin.go` + `internal/claudetoken`. A sandbox pane
whose spawn finds the token flow selected and no token runs `claude
setup-token` ON THE USER'S BEHALF: `spawnPane` calls `beginSandboxSignIn`,
which writes an explanation into the pane, returns true, and lets the spawn
stand down with **nil** — a pane waiting on something is not a pane that
failed, and the worktree placeholder is the same shape. A goroutine captures
the token, persists it, and spawns the pane.

**Standing down is what keeps this off the IPC dispatch goroutine**, which must
never block. A human authorising in a browser is minutes.

**`Options.Mirror` is nil from the daemon, and that is a security decision.**
The only mirror available there is a pane, and a pane's output is written to a
ghost buffer ON DISK — so mirroring would persist the token, the one thing this
must not do. The daemon reports progress in its own words through
`flushPaneOutput` instead.

**The single-flight is daemon-wide and RECORDS its waiters.** What it guards is
one browser window and one person's attention, not a per-pane resource — a new
tab in a two-sandbox-pane workspace creates both at once. A waiter that were
merely refused would sit with a message and never get a child, so `end()` hands
the queued pane ids back and the winner spawns them too.

**A missing `claude` on PATH is NOT a refusal.** It falls through to the
in-container sign-in: the container has its own claude and does not need the
host's, so refusing would turn a missing host tool into a pane that cannot open
at all.

`findClaudeFn` / `captureTokenFn` / `signInSpawnFn` are seams for the call-site
test. **`signInSpawnFn` is nil by default and resolved at CALL time** — a
closure over `spawnPane` in the initialiser is an initialisation CYCLE, since
`spawnPane` reaches this file. Verified by mutation: disabling the call in
`spawnPane` leaves every logic test green and only
`TestSpawnPane_SandboxWithNoTokenStartsTheSignIn` fails.

**`Capture` returns on the TOKEN, not on process exit.** `claude setup-token`
prints the token and then keeps running, so waiting on the process sat for the
whole five-minute budget and then reported failure for a sign-in the user had
already completed — the pane black throughout. Both signals are needed: `found`
for the normal case, `done` because a command that exits WITHOUT printing one
has failed and must not be waited out either. `Options.Args` exists so a test
can drive the real PTY and scanner against a command it controls; verified by
mutation, where removing the `found` arm hangs the test for its full timeout.

**Anything a spawn writes into a pane must broadcast the state FIRST**
(`Daemon.announce`). Both `constructPaneAt` and `replacePaneAt` call
`spawnPane` BEFORE their `broadcastState`, so a `pane_output` frame emitted
from inside a spawn names a pane id no attached client has seen — and the
client drops it. That is why the first version of the sign-in left a black
rectangle after the browser step, with the message sitting unread in the
daemon's buffer.

**`respawnAfterSignIn` refuses to spawn once the daemon is stopping.** The
flight is an untracked goroutine holding a five-minute budget on a human in a
browser, so a stop can land while it waits; spawning after that point starts
PTY children behind the final snapshot — processes nothing records, reaps or
can find again. `d.stopping()` reads the shutdown channel and treats a NIL one
as running, which is what the many tests that build a `Daemon` literal need.

**Shutdown deliberately does NOT wait for the flight** (`sandboxSignIn.inFlight`
exists, and `Stop` does not call it). Holding the stop path open for a browser
step turns every quit during a sign-in into a SIGKILL; the `stopping()` check is
the guard instead. Its real caller is a TEST: `beginSandboxSignIn` returns as
soon as the goroutine is launched, so a test that stubs `captureTokenFn` and
returns restores that package var while the goroutine is still reading it. That
is a data race `go test -race ./...` catches and `dev.sh test` does not — it was
live on this branch for several commits. `Add` runs BEFORE the `go` statement,
never inside it, or a `Wait` racing it returns immediately and guarantees
nothing.

## A token authenticates; it does NOT skip onboarding

`seedClaudeConfig` (`sandbox_claudeconfig.go`) writes a first-run
`.claude.json` into a pane's Claude config directory. Without it a forwarded
token looks broken: `claude -p` in a fresh container answers correctly with
nothing but the env var — measured — while INTERACTIVE Claude Code on an empty
config directory runs first-run onboarding, whose second screen is a sign-in.
Every sandbox pane gets its own config directory, so the user met that on every
pane and reasonably concluded the token was not working.

Each key answers one screen, and each was verified against the real image by
running the container and reading what came up, not guessed:

| config | what the pane shows |
|---|---|
| empty | theme picker, then sign-in |
| `hasCompletedOnboarding` + `theme` | trust prompt |
| + per-project `hasTrustDialogAccepted` | bypass-permissions banner |
| + `bypassPermissionsModeAccepted` | a working prompt |

The `projects` entry is keyed by the path the CONTAINER sees (`ContainerCWD`),
not the host's, and needs the fuller shape it has — a minimal entry made Claude
rewrite the file and the theme picker came back.

**It never overwrites.** Once Claude has written the file it is the pane's own
state, and under `shared_claude_config` it belongs to every sandbox pane at
once — clobbering it discards a real sign-in, the theme and the trust answers.

**A restarted daemon ADOPTS the saved token; it must never mint a second one.**
`adoptPersistedToken` reads the OS store when the process inherited nothing. A
daemon's environment comes from whatever spawned it — normally the TUI — and
the TUI's own environment is a snapshot from ITS start, so a token saved after
that point is invisible to both however many times the daemon restarts (Windows
builds a child's environment from its parent's and never re-reads the
registry). Without it the sign-in ran on every restart, and each `claude
setup-token` mints a NEW long-lived token that SUPERSEDES the one already-running
panes are holding — observed three times in one session, with a live pane losing
its credential. Checked before `needsSandboxSignIn` concludes anything.

**The seed is GATED on the pane actually receiving a credential**
(`sandboxTokenAvailable`, the same condition `sandboxIdentity` uses for
`ForwardOAuthToken`). Skipping onboarding also skips the SIGN-IN SCREEN inside
it, so seeding an unauthenticated pane hands the user a working-looking prompt
that fails on its first request with no way to log in — measured, and a
regression the seed introduced for `auth = "browser"` before the gate existed.
In that mode the onboarding login is not a nuisance to skip past; it is the
entire sign-in.

## What token auth COSTS, measured

A `setup-token` credential authenticates as **"Claude API"** — Claude Code says
so in its own banner — not as the subscription. Measured consequences in a
sandbox pane: **Fable 5.1 is absent from `/model`** (the list is Default /
Sonnet / Opus / Haiku) and **Remote Control reports the login expired**. Same
Claude Code build as the host, so it is the credential and not the image.

`shared_claude_config = true` with `auth = "browser"` is the other side of that
trade: one directory mounted over `/quil/claude` for every pane, so the browser
sign-in happens ONCE ever rather than per pane, and the pane gets full
subscription auth. The cost is the documented one — every sandbox pane then
shares one trust domain.

## The sign-in mode is PER PANE

`SandboxSpec.Auth` on the wire → `Pane.SandboxAuth` (persisted) →
`Daemon.paneAuthMode`, the SINGLE reader. The create dialog offers a two-way
choice under the sandbox switch; empty means "follow `[sandbox] auth`", which
is what every older client, every non-dialog producer and every pre-choice
snapshot sends.

Per-pane because the trade is per-pane and both halves are measured: a token
pane needs no sign-in but authenticates as "Claude API" (no Fable, no Remote
Control), while a browser pane signs in inside its own container and gets the
full subscription. Neither is right for every pane.

**The token half of that trade is NOT per-pane, though, and the row says so.**
One pane picking it persists a credential every later process inherits, so the
choice is per-pane in what it costs this container and machine-wide in what it
costs every other Claude — which is why `browser` leads `sandboxAuthChoices`
and why the token option's detail line names the reach rather than only the
lost features.

**Everything that decides how a container authenticates goes through
`paneAuthMode`** — `needsSandboxSignIn`, `sandboxTokenAvailable` and
`dockerCLIEnv`'s caller. Splitting them lets the radio and the config disagree
about one pane, which is either a pane that cannot authenticate or one that
silently loses the model it was opened for.

**`applySandboxSpec` validates the mode like the image.** Any IPC client can
set it, and the two modes hand the container different credentials, so an
unknown value drops to empty — follow the config — rather than being stored and
acted on. Pinned by `TestApplySandboxSpec_RecordsTheAuthChoice`; a mutation
dropping the wire value passes every other test in the package.

An unrecognised value read back from a SNAPSHOT falls back to the config too,
so a future version's mode cannot pin a pane to something this build has no
implementation for.

## Codex and opencode ARE supported; their credentials are not Claude's

`plugin.UsesClaudeAuthName` is the single predicate, and it gates FOUR things
that must agree: the sign-in (`beginSandboxSignIn`), the token forwarding
(`sandboxIdentity`'s `ForwardOAuthToken`), the docker CLI env
(`dockerCLIEnv`'s caller), and the dialog's sign-in row
(`showSandboxAuthField`).

Without it a **codex** pane with no Claude token ran `claude setup-token` and
opened a browser for an account it does not use — `beginSandboxSignIn` took no
plugin at all. The container also received a `CLAUDE_CODE_OAUTH_TOKEN` it has
no use for, and its Claude config was seeded with answers to first-run screens
it never shows.

The sandbox row itself STAYS for both: a container is exactly as useful for
codex or opencode, and the daemon has always branched for them
(`hookModeFor`, the opencode hook-script copy). Only `linuxQuildForPane`'s
refusal is claude-only, deliberately — claude-code cannot run hookless (exit
129 on the next restart) while the other two lose only notifications.

`scripts/sandbox-image.sh --with codex,opencode` installs them; the verifier
then checks EVERY requested agent with `--version`, because a pane whose plugin
binary is missing dies at spawn and a clean build log cannot tell you that.
Measured together: claude 2.1.263, codex-cli 0.153.4, opencode 1.18.29.

## Codex signs in by a COPIED credential, and that is a different posture

`seedCodexAuth` copies the host's `~/.codex/auth.json` into the pane's own
`CODEX_HOME` (`/quil/codex`, per pane). MEASURED before it was written: a
container given that file runs `codex exec` against the user's own plan and
answers. Without it every codex pane opens on Codex's three-way sign-in menu,
whose browser option cannot work from a container at all — the OAuth callback
goes to a localhost the host browser cannot reach.

**Quil copies a codex credential and never a Claude one, deliberately.**
`claude setup-token` exists to mint a token for exactly this purpose and
Anthropic's authentication policy speaks directly to intermediating theirs;
codex ships no equivalent minting command, and copying its auth file is the
mechanism its own users use for containers.

A COPY, not a mount, for two reasons: the container must not be able to write
back over the host's credential, and a bind-mounted file cannot be replaced
from inside when codex refreshes its token.

**Never overwrites** — once the pane has an `auth.json` it is the pane's own and
codex rewrites it on refresh. **A missing host credential is not an error**: the
pane shows Codex's own sign-in, correct for someone who has never run codex
here. The seed is gated on the codex plugin, and BOTH directions are pinned —
never called leaves a codex pane stranded, called for everyone puts a credential
in panes that have no use for it.

`CODEX_HOME` is set for every sandbox pane regardless of type, so a codex run by
hand inside a claude container does not fall back to a home nothing mounts.
