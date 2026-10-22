---
description: Docker sandbox panes — the mount set as a security boundary, the per-pane object store and its ordered teardown, the sign-in flows, and refusals that never soften.
paths:
  - internal/sandbox/**
  - internal/sbx/**
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

**The script passes EXACT agent versions, never `latest`** (`exact_version`, asked of npm inside the base image). Docker keys a `RUN` layer on its text, so `npm install pkg@latest` is the same line on every build and a rebuild reused the first build's layer — the image stayed on that agent version forever while the script reported success (found 2026-10-03: an image at Claude 2.1.263 with 2.1.288 out). An exact number changes the line when a release lands and keeps the cache when none has. The same holds for `CODEX_VERSION`/`OPENCODE_VERSION`. "Exact" is a FULL semver match (`EXACT_SEMVER`), never a glob: `[0-9]*.[0-9]*.[0-9]*` also accepts `2.1.0 - 2.1.999` and `2.1.0 || 2.1.289`, which then reached the build unresolved. `scripts/test-sandbox-image.sh` (run by `ci.yml`, stub docker, no network) pins it. The image sets `DISABLE_AUTOUPDATER=1`: the package is root-owned and every pane starts a fresh container, so self-update can only fail or vanish; updating means rebuilding.

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

## Changing the mode under a directory has to REPAIR it

`sandbox_authstamp.go`. The two modes leave INCOMPATIBLE state behind:
`seedClaudeConfig` writes `hasCompletedOnboarding` so a token pane skips four
first-run screens, and **the sign-in is one of the screens that answer skips**.
Move the same directory to the browser flow and the token is gone while the
answer stays — a prompt with no credential and no way to log in. Flipping the
default did that to every existing pane at once, which is how it was found.

`prepareSandbox` therefore calls `reconcileClaudeAuthMode` **before**
`seedClaudeConfig`, and the order is load-bearing: the seed never overwrites,
so a stale answer it would decline to touch has to be repaired first.

- The stamp (`.quil-auth`) lives **beside the config**, not on the `Pane` —
  under `shared_claude_config` one directory serves every sandbox pane, so a
  per-pane record says nothing about the file being repaired.
- **Only token → browser repairs.** The other direction gains a credential, so
  the answer on disk is still right and a repeated sign-in buys nothing.
- **No stamp + an existing config = token era.** That inference is why the
  stamp alone is not enough: panes created before it existed have no record and
  are exactly the ones the default change stranded. It is imprecise in one
  direction — an explicit pre-stamp `auth = "browser"` user who signed in is
  asked once more — and that is the better failure, because an extra sign-in
  explains itself while the state it replaces does not.
- Only `hasCompletedOnboarding` is deleted. The file also holds the theme,
  per-project trust answers and history, and under `shared_claude_config` it
  holds them for every pane. A config that does not decode is left ALONE.
- Nothing here fails a spawn. The repair restores a sign-in screen; refusing to
  open the pane would be worse than the screen it is restoring.
- Reads go through `os.Root`, like every daemon-side read under a pane's tree.

Gated on `plugin.UsesClaudeAuthName` with the rest of the auth decisions — a
codex directory holds no Claude onboarding answer and must not be stamped with
a Claude sign-in mode. Pinned at the CALL SITE by
`TestPrepareSandbox_ReopensOnboardingWhenTheModeDropsTheToken` and its two
mirrors; verified by mutation, since the unit tests stay green against a
`prepareSandbox` that never calls any of it.

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

That announce runs INSIDE the lazy spawn too — `ensurePaneSpawned` holds the
pane's `spawnMu` across it — so nothing the state builder takes may be
`spawnMu`: `Pane.Pending` is an atomic for exactly that reason (2026-10-22,
v1.93.0: a project switch onto a deferred sbx pane froze every broadcast on
the daemon). See `daemon-lifecycle.md`'s "Workspace restore".

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

The **Shared** choice (browser auth + `claude_config: "shared"`; the default
when `shared_claude_config = true`) is the other side of that trade: one
directory mounted over `/quil/claude` for every Shared pane, so the browser
sign-in happens ONCE rather than per pane, and the pane gets full subscription
auth. The cost is the documented one — every Shared pane shares one trust
domain. See "The Claude config directory is PER PANE too" above.

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

## The Claude config directory is PER PANE too (#251)

`SandboxSpec.ClaudeConfig` (`"own"`/`"shared"`, empty = follow
`[sandbox] shared_claude_config`) → `Pane.SandboxClaudeConfig` (persisted as
`sandbox_claude_config`) → `sharesClaudeConfig(choice, cfgShared, plugin)`
(`internal/daemon/sandbox_shared.go`), the ONE rule. It replaced a daemon-global
switch that the resume path recorded once at start.

**The mount and the resume path must read the same decision.**
`prepareSandbox` sets `SharedClaudeRoot` from it and `hostTranscriptPath` maps
`/quil/claude/...` through it; splitting them mounts one directory and looks
for the transcript in the other — exit 129. The resume path has no `Daemon`,
so it reads `sharedClaudeConfigDefault` (a BOOL set at start) plus the pane's
own choice, and derives the path with `sharedClaudeConfigDir`. The old package
var held the DIRECTORY and was set only when the config was on, so a `"shared"`
pane on a config-off daemon had nothing to map to.
`TestHostTranscriptPath_FollowsThePaneChoice` pins both directions.

**A non-Claude agent never gets the shared directory** — rule 1 of
`sharesClaudeConfig`, gated on `plugin.UsesClaudeAuthName` like the sign-in,
the token, the CLI env and the stamp. Before #251 the global switch mounted it
read-write into codex and opencode containers too, so either could plant a hook
every shared Claude pane then ran. Verified by mutation
(`TestSharesClaudeConfig`, `TestPrepareSandbox_SharedRootFollowsThePaneChoice`).

**The TUI row is a three-way CHOICE, not a mode** (`sandboxAuthChoices`:
browser / shared / token) and `sandboxSignInFields` maps it onto both wire
fields, so `"shared"` never reaches `Auth`. Token sends `claude_config: "own"`:
a dialog-made token pane never enters the shared directory, so that directory
serves browser panes only and its `.quil-auth` stamp cannot alternate. **The
row always sends what it displays**, untouched or not — an empty `Auth` used to
resolve through the DAEMON's config, which for a remote project is another
machine's, so the row could show one mode while the pane got the other.
`defaultSandboxSignIn` is the one config→choice mapping (token wins over
shared), shared by the dialog and F1 → Settings → Sandbox, and the page writes
BOTH keys for every choice so it can never write `token` + `shared`.

**A token pane never shares, decided on the EFFECTIVE values at creation**
(`Daemon.applySandboxSpecFor`, the create-path wrapper of `applySandboxSpec`).
Either field may be empty and filled in from the daemon's config — an MCP create
naming only `shared` on a daemon configured `auth = "token"`, or naming neither
on a daemon configured token + shared — so checking the literal wire pair
missed both (review findings on PR #252). Such a pane is recorded `own`. Only
creates run it: a restored pane keeps what its snapshot recorded, empty
included, so an upgrade moves nobody's transcripts — which means a pre-#251
token pane under a token + shared config stays in the shared directory.
`TestSettleSandboxSharing_UsesTheEffectiveValues`,
`TestHandleCreateTab_TokenSharedConfigRecordsOwn` (mutation-checked).

**A field cannot be version-gated by NUMBER, so it is gated by the daemon's
own answer.** An older daemon IGNORES `claude_config`, and the dangerous
direction is the reverse of the obvious one: with its own
`shared_claude_config` on, an explicit `own` lands in the shared directory. So
`ipc.FeatureSandboxClaudeConfig` is listed in `ipc.GatedRequests` and the MCP
bridge's `requireCreateFields` sends the field only to a daemon whose Requests
list names it (both `create_pane` and `create_tab.first_pane`). It fails
CLOSED, unlike `requireRequest`: a daemon with no list — an unstamped `dev`
build or an unknown version included — is refused, because there refusing is
the only way to keep the isolation the caller asked for (Greptile, PR #252).
Release TUIs are exact-match gated; a dev TUI against an older daemon is the
accepted gap.

**The F1 page owns paste on every row** (`pasteIntoSandboxSettings`, a branch in
`Update`'s `tea.PasteMsg` arm). The generic `dialogEdit` arm only catches a
paste while a field is being edited, so on this page a paste outside edit mode
fell through to `sendClipboardToPane` and ran in the hidden pane. The image is
remembered only at the three points that actually SEND a create, never for one
refused locally.

**The image is remembered per destination, client-side**
(`config.SandboxImagePath`, `internal/tui/sandbox_image_store.go`), written on
submit under `createPaneDialogDest()`, never in `config.toml` (whose `Save`
rewrites the file). Disk access goes through `SetSandboxImageStore`, installed
by `cmd/quil/main.go`; a `Model` built in a test has none. Unlike recent CWDs
under `shared_data` it is not shared between two TUIs on one daemon — accepted.

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

## The sbx backend is a different shape, and its boundary is the TRUST PIN

`internal/sbx` + `internal/daemon/sandbox_sbx.go`. `SandboxSpec.Backend = "sbx"`
→ `Pane.SandboxBackend` (persisted as `sandbox_backend`, with the same
`sandbox/` type prefix, so an older daemon refuses rather than spawning on the
host). An sbx pane has NO `SandboxImage`, so every docker-only path (mount set,
object store, teardown, harvest, shutdown kill) skips it by construction.

**Quil builds no cage here.** The cage is the sbx environment file and kits the
project's `.quil-sandbox.toml` names. Because that file lives in the repository
the agent can write, `loadSbxProfile` refuses a profile whose digest
(`sbx.Digest`: profile, env file, every file under each LOCAL kit, the
post-create env file, script arguments that are files) differs from the one
`quil sandbox trust` recorded. Checked on EVERY spawn, the ready respawn
included — a profile edited after approval must not point a pane at another
sandbox. Never soften this into "run anyway".

**A symbolic link in the pin is REFUSED, never hashed** (`refuseLinks`, from
the file up to the profile directory, and every kit entry): hashing the link
text let an agent change the target with the digest unchanged (PR #260 review).
**`prepare_host` may name ONE project file, its script** (`fileArgs`, any
spelling — an absolute path into the repository was once exempt and ran
unpinned). An absolute prepare_host argv[0] outside the profile directory is
REFUSED, not trusted as a system program: the worktrees folder beside the
repository is shared with the agent, so a direct script there would run
unpinned. A bare name goes through PATH (Go refuses a cwd lookup). post_create
keeps the exemption — its argv[0] names a program inside the VM. A second file is refused because the copy cannot tell an input from
an output (`cp a b` wrote into the copy and lost the result).

**Nothing approved is read from the repository again.** `sbx.Approve` returns
`sbx.Approved`: the bytes `readPinned` read once, and the Profile PARSED FROM
THOSE BYTES (`parseProfile`), refused unless equal to the caller's parse — the
earlier parse could come from a changed file restored before the digest read.
`Ensure` runs `prepare_host` from a private 0700 copy of the script
(`hostArgv`), the script chosen from the APPROVED snapshot rather than
the disk (a script removed after approval was skipped, its repository path
passed on, and a file put back in time ran), `recheck`s (the host step must not change a pinned file), then
`stageEnv` writes the env file, local kits and post-create env file to
`$QUIL_HOME/sandbox/sbx-env/<name>/` (persistent: sbx keeps the env file
mounted) and creates from that. Measured on sbx 0.48: relative paths,
`${{ env.fileDir }}` and `${{ env.projectDir }}` all resolve to the env
file's folder when a path is named, so `rewriteEnv` expands both tokens to the
original folder and makes relative `workspace`/`additionalWorkspaces` paths
absolute; kits keep their relative layout under one common root. Any other
relative path lands in the copy — a broken create, never a wider cage. An env
file with `lifecycle:` is refused (`errLifecycle`): Quil's `--force` approves
sbx's host commands, and the scripts they call are not pinned. It is checked by
`envKitDirs`, which the pin and the copy share; `parseEnv` reads a
`yaml.Node`, accepts a kit as a bare string or a `source` mapping, and FAILS
CLOSED on anything else (a typed decode once failed on a string kit and
skipped the lifecycle check). YAML merge keys (`<<`) and aliases are refused
(`refuseIndirection`): Quil reads the document as written while sbx decodes it
with them applied, so a root `<<:` hid a lifecycle block. A relative workspace
path still holding `${{ … }}` after the folder tokens is refused at approval:
it may expand to an absolute path, and anchoring it would mount another
folder. A host script
must not find siblings through `$0`.

**The create is minutes, so it is off the dispatch goroutine**, shaped like the
sign-in: `startSbxPane` stands down (nil error), `beginSbxPrepare` single-flights
per sandbox NAME, the goroutine runs `sbx.Client.Ensure` with output written into
the owner pane, then respawns every waiter. **The claim is held until the LAST
waiter is started** (`sbxPrepare.next` loops until nobody is waiting): released
after the create, a restart during the mount/stop/respawn started a second
preparation and two agents. Only the respawn may start the agent, and it says
so on the SESSION (`sbxReadySession`), not on the pane — a pane flag could be
taken by a racing restart. Each waiter carries the pane's `ptyGen` from when it
asked; a pane whose generation moved (started since) is dropped at the START
of `startSbxWaiters`, before the leftover prefix stop — dropped only at the
respawn, the stop killed the agent the earlier batch had just started.
An existing sandbox is reused untouched; closing a pane never removes one.

**The agent runs through `sbx exec -it -w <cwd> <name> <cmd> <args>`**, never
`sbx run`: exec runs the pane's own plugin command and lets panes share one
sandbox. No hooks, no `--settings`; `resolveSpawnArgs` is not called for an sbx pane.

**The claude session of an sbx pane is kept by Quil, not by a hook**
(`internal/daemon/sandbox_sbx_resume.go`, `internal/sbx/resume.go`). A
first start mints a session id into `PluginState["session_id"]` (persisted
in `workspace.json` — the host keeps the id, the VM keeps the transcript)
and passes the plugin's `start_args` (`--session-id <id>`). Every later
start — Alt+R, a daemon restart, a restored workspace — asks the VM whether
`<id>.jsonl` exists under claude's config dir (`sbx.Client.HasTranscript`:
`sbx exec [-w <cwd>] [-e <plugin env>…] <name> sh -c <script> sh <id>`, the
id an ARGUMENT, a marker line the answer, `CLAUDE_CONFIG_DIR` honoured,
`~/.claude` the default) and passes `--resume <id>` when it does,
`--session-id <id>` again when it does not. **The probe runs with the
agent's own folder and plugin environment** — a plugin env naming
`CLAUDE_CONFIG_DIR` (even relative) would otherwise make the probe look in
the default folder, miss, and have the restart refused (PR #272 review,
M-1). **The guard against a second session selector reads the EFFECTIVE
args** (instance args, else the plugin's own): a plugin whose `args` carry
`--continue` gets no `--session-id` (M-2). **After the probe the pane is
re-checked** (`sbxSpawnStillWanted`: daemon not stopping, pane still
registered, `ptyGen` unchanged) before `Start`: the probe can wait tens of
seconds, and a pane closed meanwhile was torn down with no PTY to close, so
an agent started then would run in the VM with nothing owning it (M-3). The
probe's context ends with the daemon (`sbxPrepareContext`). **Neither argument may be the default**: claude refuses `--resume` for
an id it never persisted (a restart on the trust screen) and `--session-id`
for one it did (exit 129), which is why the earlier "never pass a session id"
rule existed. A failed probe FAILS the start with the reason on the pane —
never a guess. The resume goes through `claimResumeSession`, so two panes
cannot run one conversation; a held session gives the pane a fresh id.
`resume_session_id` (an MCP create) is adopted as the pane's own id and
probed like a restart. A persisted id that is not a UUID is replaced, never
handed to the VM. `transcript_path` is deleted on an sbx pane: nothing on
the host names the VM's file. It runs in `startSbxPane`'s ready branch, on
the preparation goroutine, because it is a VM round trip. **Blind spot**: a
`/clear` or `/resume` inside the agent rotates the session and no hook
reports it, so a restart reopens the conversation from before the rotation.
Where the transcript lives is the sandbox's business: sbx keeps
`~/.claude/projects` on a per-sandbox volume (measured on 0.48: it survives
`sbx stop`/start and dies with `sbx rm`); the tool that owns the sandbox
decides whether it survives a recreate. Mounting a host folder as
`CLAUDE_CONFIG_DIR` for every sandbox was considered and rejected with the
owning tool (cf-manager): several sandboxes would write one `.claude.json`
and settings would bleed between tickets.
The spawn log line passes argv through `sbx.RedactEnv`: plugin env rides
`-e KEY=VALUE`. `-w` is omitted on a Windows host. Since 2026-10-16 the create, reuse and exec
path has run against a live sbx 0.48 on macOS.

**Worktrees: one sandbox per REPOSITORY, the worktrees folder mounted live.**
`sbx.FindProfileFor` (shared with `quil sandbox trust`, so the user approves
the profile the daemon checks) sends a pane in a linked worktree to its MAIN
checkout's profile (`sbx.FindCheckout` — file reads only, no git process, because
`startSbxPane` runs on the dispatch goroutine), so a branch editing its own copy
cannot repoint its panes, and the worktree shares the main checkout's VM and
sign-in. After `Ensure`, `mountSbxWorktrees` runs `sbx mount <name>
gitworktree.WorktreesDir(main)` for every waiting pane whose folder exists and
is a REAL directory (a symlink would widen the cage to wherever it points)
and whose main checkout is the profile directory or below it, compared as
REAL paths (`EvalSymlinks`) and mounted as the real folder: a link above the
checkout kept the path lexically inside. `FindCheckout`
accepts a linked worktree only when the MAIN repository lists it (the admin
dir sits in `<main>/.git/worktrees` and its `gitdir` points back) — the pointer
files in the worktree are agent-writable, and forged ones made Quil mount
another repository's worktrees folder. The
FOLDER, not each worktree, so a worktree made later is visible with no step.
Measured on sbx 0.48: the mount applies to the running VM in ~0.2 s, a process
already running inside keeps running, a repeat is a no-op success, and a
worktree added afterwards is visible with `git status` working. It runs on
every spawn because every spawn goes through the prepare goroutine. A failed
mount fails only panes INSIDE the folder; a main-checkout pane starts with a
note. `sbx ls` does not list a mount added this way — do not try to detect it
there; rely on the idempotent repeat.

**Attach mode: a sandbox another tool owns.** `SandboxSpec.Name` →
`Pane.SandboxName` (persisted `sandbox_name`, validated by `sbx.ValidName` at
`applySbxSpec`; a docker spec with a name is refused). `sbxTargetFor` picks the
mode: a name means NO profile, NO trust pin, NO create, NO `sbx mount`, NO
`sbx rm` — the user picked the sandbox in the dialog, so nothing from the
repository decides anything, and Quil never widens a cage it did not build.
An invalid persisted name is REFUSED, never dropped (dropping would move the
pane into the profile's sandbox). The preparation is keyed `attach:<name>`
(`sbxTarget.key`), apart from a profile preparation of the same name, so the
two never share a waiter list with different checks. `findSbxSandbox` reads
`sbx ls --json` (`sbx.ListSandboxes`/`ParseListJSON`, measured on 0.48: a bare
list, workspaces as `path` or `path:ro`, primary first) and refuses a missing
name; `checkSbxAttach` refuses a pane folder not in a WRITABLE workspace —
without it `-w` would start the agent outside what the user opened.
`Listed.Covers` compares real paths and lets the MOST SPECIFIC workspace
decide (a `:ro` folder inside a writable one stays read-only; at one path
read-only wins). Because `sbx ls` omits folders added with `sbx mount` (see
the worktrees note above), a "no" from `Covers` is not final:
`sbx.WritableInside` runs `sbx exec <name> sh -c 'test -d "$1" && test -w
"$1"' sh <dir>` (the folder an ARGUMENT, a marker line as the answer;
`test -w` is access(W_OK), so it reports EROFS on a read-only mount). A
folder the agent made inside the VM also passes — that cannot widen the
cage, the pane still runs in that sandbox. On a Windows host the probe is skipped
(`sbxHostOSFn`, the same seam as the exec's `-w`): a Windows path names nothing
in the VM, and without `-w` the agent starts in the main workspace anyway, so
the pane starts with a note. Measured on 0.48: `sbx exec` starts a
stopped sandbox itself, so attach needs no start step. The leftover prefix
stop still runs: it matches only this pane's own tag. The create dialog's
list rides the capability answer (`SandboxCapRespPayload.SbxSandboxes`, a
fresh `sbx ls --json` per request, writable and read-only folders — no new
message type, so the rights table and web allow-list are unchanged); the TUI
ORDERS it by TEXT against the browsed folder (`sbxListsFolder` /
`folderHolds`, most specific wins, either separator, since the daemon may be
another OS): listed fits first, the rest after them and still selectable,
since only the spawn-time probe knows about `sbx mount` folders. When ←/→ lands on a sandbox whose listing does not hold the
browsed folder (or no folder is chosen), the dialog moves the folder to its
first writable workspace (`followSbxAttachFolder`) — a pane opened in the
repository root above a ticket worktree was otherwise refused at spawn. Only
the arrows do it, never submit, so a `sbx mount` folder can still be browsed
back to. Quil and
the owning tool know nothing of each other: the only shared facts are the
sandbox name and sbx's own listing.

**Ending the host `sbx exec` does NOT end the agent inside the VM** (measured:
a closed pane's claude ran for a day). Every exec carries `-e
QUIL_SBX_PANE=<pane id>-<random>` (`sbx.NewTag`, PER SPAWN — a restart closes
the old PTY on a goroutine while the new spawn runs, so a pane-wide tag would
let the old stop kill the new agent). `startSbxPane` returns the PTY wrapped
in `sbxSession`, whose `Close` (once) runs `stopSbxTagAsync`: `sbx exec <name>
sh -c stopScript sh <tag> exact`, which scans `/proc/*/environ`, TERMs, waits
2 s, KILLs. The tag is an ARGUMENT, never script text. Close is the one funnel
for destroy, replace, restart and daemon Stop (which then waits
`sbxStopWait`). A killed daemon never runs Close, so `runSbxPrepare` runs a
PREFIX stop (`<pane id>-`) for each pane before respawning it; the new tag is
minted after, so it cannot match. Measured on sbx 0.48: 0.46 s per stop, exact
and prefix both touch only their own processes.

**A pasted image reaches an sbx pane through the daemon** (`paste_image_req`,
`internal/daemon/paste_image.go`, `internal/sbx/paste.go`, TUI side
`internal/tui/paste_image.go`). The ordinary paste saves a PNG on the TUI's
machine and types its host path, which names nothing in the VM. For a pane
whose `SandboxKind` is sbx the TUI sends the bytes instead (the TUI may be on
another machine), the daemon `sbx cp`s them to `/tmp/quil-paste-<time>-<hex>/image.png` in the
sandbox the RUNNING session names (`sbxSession.name` — never re-derived from a
profile), then runs `chmod` as root: measured on 0.48, `sbx cp` leaves a
root-owned copy with the source's mode, and the agent (uid 1000) is refused a
0600 file. The folder is NEW per paste, directly under `/tmp`, with an unguessable name:
the chmod runs as root inside a VM the agent controls, and a fixed folder
(`/tmp/quil-paste`) could be made — or replaced by a link — by the agent before
the first paste, pointing the root chmod anywhere (PR #271 review). Root owns
the new folder and `/tmp` is sticky, so the agent cannot swap it afterwards.
The answered path is typed with `enqueueInput`, NOT through the typing guard:
the image exists only in the asking pane's sandbox. **The upload is bound to the
host the pane was on when the user pressed paste** (`sandboxImagePasteMsg.dest`,
resolved on the Update goroutine BEFORE the clipboard read): `startSandboxImagePaste`
refuses when the pane is gone or on another host, and never calls `destOfPane`,
whose missing-pane fallback is the ACTIVE host — a screenshot would otherwise
follow the user to a machine they never aimed it at (PR #271 review, H-1). One image, one sandbox — mounting the paste folder was rejected
because every sandbox would see every screenshot. Daemon-wide single-flight
(`pasteCopying`); size and PNG magic are refused before the slot. The TUI
types only a path `sbx.ValidPastePath` accepts, since
the answer can come from another machine and is typed into a PTY. Docker
sandbox panes still get the host path (`techdebt/3-2-docker-sandbox-pane-image-paste.md`).
