# Sandbox panes

Run an AI pane inside a local Docker container, with its git checkout
bind-mounted in. The agent edits your real files and its commits land in your
real repository, but its filesystem reach stops at that checkout.

Quil publishes no container image. You supply one, and [Building the
image](#building-the-image) is a script that builds it for you.

---

## Prerequisites

| What | Why |
|---|---|
| **Docker, running linux containers**, on the machine the **daemon** runs on | The daemon spawns the container. Under `quil --remote host`, that is `host` — not the laptop you type on. Docker Desktop in Windows-containers mode answers the probe and then fails every image, so Quil treats it as unavailable |
| **An image** with the agent binary, `git`, and a non-root user | Quil publishes none. Build one with `scripts/sandbox-image.sh` |
| **Windows only:** the drive holding your repository shared in Docker Desktop | Docker Desktop cannot bind-mount a drive that is not shared. The pane fails to start |
| **A git checkout** — the directory you pick must be inside a repository | The mount set is built out of the checkout and its `.git`. A plain directory fails at spawn with `not a git repository` |

Nothing else is required. No `--privileged`, no `NET_ADMIN`, no extra
capability, and no daemon config.

If the row **Run in a Docker container** is missing from the pane dialog, the
daemon could not find a usable Docker. A line in its place shows the reason the
daemon reported.

---

## Using it

1. `Ctrl+N` (split) or `Ctrl+T` (new tab).
2. Pick an AI plugin — **Claude Code**, **Codex** or **OpenCode**.
3. Choose the directory.
4. Turn on **Run in a Docker container**.
5. Type the image name, or leave the pre-filled one. Quil remembers the last
   image you used on each host (local or remote) and fills it in next time;
   before the first one, it uses `[sandbox] default_image`.
6. **Claude Code only:** pick **Sign in** — `Browser` (default), `Shared` (sign
   in once for all Shared panes) or `Token`. See [Signing in](#signing-in).
7. Press **Continue**.

Closing the pane removes its container. Restarting the pane (`Alt+R`) builds a
new one.

### Which agents can be sandboxed

| Plugin | Sandbox | Sign-in inside the container |
|---|---|---|
| **Claude Code** | Yes | Browser once per pane (default), once for all Shared panes, or a forwarded token — you choose per pane |
| **Codex** | Yes | None. Quil copies your host `~/.codex/auth.json` into the pane |
| **OpenCode** | Yes | Once per container, in the container |
| Terminal, lazygit, k9s, … | No | The row is not offered — these are not AI panes |

---

## Building the image

```bash
scripts/sandbox-image.sh
```

From PowerShell on Windows, run it through the Git Bash that Git for Windows installs:

```powershell
& "C:\Program Files\Git\bin\bash.exe" scripts/sandbox-image.sh
```

That builds `quil-sandbox:latest` on your machine from
`docker/sandbox/Dockerfile`, then **verifies the result** — it asks the image
for a non-root user, a working `claude`, and `git`, rather than reporting
success from a clean build log.

Type `quil-sandbox:latest` in the dialog once — it is remembered for that host.
To pre-fill it before the first pane, set it in **F1 → Settings → Sandbox**, or:

```toml
[sandbox]
default_image = "quil-sandbox:latest"
```

One image serves all three agents when you build it with `--with
codex,opencode`, so there is one default image, not one per agent.

### Flags

| Flag | Effect |
|---|---|
| `--tag NAME:TAG` | Build under a different tag. Default `quil-sandbox:latest` |
| `--base IMAGE` | Different base image. Default `node:22-bookworm-slim` |
| `--claude-version V` | Pin the Claude Code npm version (exact, a range, or a tag). Default `latest`, resolved to an exact number before the build |
| `--with codex,opencode` | Install those agents beside Claude Code |
| `--check --tag T` | Run the verification only, against an image you already have |

Examples:

```bash
# Reproducible image for a team — pin the agent version.
scripts/sandbox-image.sh --tag team/quil-sandbox:2026-09 --claude-version 1.0.100

# All three agents in one image.
scripts/sandbox-image.sh --with codex,opencode

# Check an image you built yourself, by hand, from your own Dockerfile.
scripts/sandbox-image.sh --check --tag my-own-image:latest
```

### Updating the agents

Run the same command again, with the same `--with` list:

```bash
scripts/sandbox-image.sh --with codex,opencode
```

The script asks npm for each agent's current version and builds with that exact number, then prints what changed:

```
  claude   : 2.1.263 -> 2.1.288
  codex    : 0.160.0 (already current)
```

When nothing changed, Docker reuses its cache and the rebuild takes seconds. New sandbox panes use the new image; a pane that is already running keeps its container until you restart it (Alt+R).

The agents cannot update themselves inside a pane. Each pane starts a fresh container from the image, so an update made inside one would be gone at the next start. The image turns Claude Code's self-update off (`DISABLE_AUTOUPDATER=1`) for that reason.

The verification is the part worth keeping. A build that succeeds proves the
`RUN` lines exited zero; it does not prove `claude` is on the agent user's
`PATH`, which is the failure people actually hit.

### What the recipe does, layer by layer

`docker/sandbox/Dockerfile` is short on purpose. Each step exists for a reason
that has bitten someone.

```dockerfile
ARG BASE_IMAGE=node:22-bookworm-slim
FROM ${BASE_IMAGE}
```

Claude Code, Codex and OpenCode all install from npm, so a Node base saves a
toolchain layer. Any Debian-family base with Node works.

```dockerfile
ARG CLAUDE_CODE_VERSION=latest
```

`latest` tracks releases. Pin an exact version for an image a team shares —
that is what Anthropic's own dev-container guidance recommends, and it is what
makes two developers' sandboxes comparable.

```dockerfile
ARG AGENT_USER=agent
ARG AGENT_UID=1001
```

**The user must not be root.** Claude Code refuses to start with
`--dangerously-skip-permissions` as root on Linux and macOS, and running
unattended behind an isolation boundary is the whole point of a sandbox pane.

```dockerfile
RUN apt-get install -y git ca-certificates less ripgrep
```

`git` is **required**, not a convenience: the pane's checkout is a linked git
worktree and its object store is wired up with git commands run inside the
container. `ripgrep` and `less` are what Claude Code shells out to — without
them its search and paging degrade quietly instead of failing loudly.

```dockerfile
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

ARG AGENTS=""
RUN for a in ${AGENTS}; do … codex → @openai/codex … opencode → opencode-ai … done
```

`--with codex,opencode` sets `AGENTS`. They install in **one** layer, so an
image built without them carries no empty layers and adding one does not
reorder the cache for the others.

```dockerfile
RUN useradd --create-home --uid "${AGENT_UID}" --shell /bin/bash "${AGENT_USER}"
USER ${AGENT_USER}
WORKDIR /repo
CMD ["bash"]
```

The home directory holds nothing that matters: Quil mounts the pane's own
Claude config directory over `/quil/claude` and points `CLAUDE_CONFIG_DIR` at
it, so credentials and transcripts live on the host under
`$QUIL_HOME/sandbox/panes/<pane-id>/`, not in the image.

**There is deliberately no `ENTRYPOINT`.** Quil supplies the command; an
entrypoint would sit between docker and the agent process and break the pane's
TTY handling.

### Bringing your own image

You do not have to use this recipe. `--check` asserts the three things any
image must provide:

- **A non-root user** — see above.
- **The agent binary on `PATH`** — `claude`, `opencode` or `codex`, matching
  the plugin you pick. A missing binary shows as
  `exec: "claude": executable file not found in $PATH` and the pane exits.
- **`git`** — see above.

Quil mounts only four things into the container — the checkout, the
repository's `.git`, the pane's own directory, and its Linux hook binary
(read-only). It installs nothing at runtime, so anything your agent needs (a
JDK, a Python, your company's certificates) belongs in the image.

### Why you build it rather than pull it

There is no image to pull. Quil publishes none — shipping Claude Code
preinstalled inside a vendor image is "preinstalling or running Claude Code in
your products or services" under Anthropic's Commercial Terms, while an image
you build yourself means Quil preinstalls nothing. Anthropic publishes none
either: their guidance is a dev-container *recipe* you copy into your own
repository.

**Do not trust an official-looking registry name.** `anthropics/claude-code` on
Docker Hub is not Anthropic's. It is a security researcher's honeypot,
published to study exactly this assumption; it runs as root and contains no
Claude Code at all. Docker Hub namespaces are first-come and unrelated to the
GitHub organisation of the same name.

### Network

Quil passes no `--network` and no `--cap-add`, so a sandbox pane reaches
whatever the host can. The mount set is the boundary this feature enforces;
egress is not. If you need a default-deny firewall, start from Anthropic's
example dev container, which ships one — it needs `NET_ADMIN`, which Quil does
not grant, so that firewall has to be applied by the image's own runtime rather
than by Quil.

---

## Signing in

Each agent authenticates differently, because each vendor offers a different
mechanism. Quil follows the vendor's own container guidance in each case.

### Claude Code

Two flows, both from Anthropic's dev-container documentation, and the browser
flow comes in two forms. **Pick one per pane** on the **Sign in** row of the
create dialog:

| Choice | What it does |
|---|---|
| **Browser** (default) | Sign in inside this pane's container. Its own config directory. |
| **Shared** | Sign in once; every other Shared pane reuses it. They share one config directory — see [Signing in once](#signing-in-once-instead-of-once-per-pane). |
| **Token** | No sign-in. Read the warning below first. |

Set the default in **F1 → Settings → Sandbox**, or in config:

```toml
[sandbox]
auth = "browser"              # or "token"
shared_claude_config = false  # true = Shared is the default
```

The default is a setting of the client you create the pane from: the dialog
sends the choice it shows, so it applies at once and for remote projects too.
Existing panes keep the choice they were made with. The daemon reads the same
keys at its own start, but only for panes created without a choice (MCP
`create_pane` without the sandbox sign-in fields, or an older client).

`auth` accepts `"browser"`, `"token"`, and the empty string, which means
`"browser"`. Anything else — a typo, the wrong case — also resolves to
`"browser"` and is reported in `quild.log`. Only the exact string `"token"`
selects the token flow, because that is the one that writes a credential
outside the pane. The dialog row overrides the config for that pane only.

| | **Browser** (default) | **Token** |
|---|---|---|
| Set-up cost | Once per pane | Once per machine, automatic |
| What runs | `claude`'s own sign-in, inside the container | `claude setup-token`, driven by Quil |
| Authenticates as | Your subscription | "Claude API" |
| Fable in `/model` | Yes | **No** |
| Remote Control, claude.ai connectors | Yes | **No** |
| Affects non-sandbox panes | No | **Yes — see the warning below** |

> ### The token flow is not contained by the pane
>
> `claude setup-token` mints a credential, and Quil saves it to your **OS user
> environment** — it keeps no copy of its own, by design. Every process started
> after that inherits it, including the daemon, and therefore **every ordinary
> Claude pane the daemon spawns**. Claude Code prefers that token over an
> interactive login.
>
> So picking **Token** once, for one sandbox pane, moves *every* Claude pane on
> the machine onto "Claude API": a smaller `/model` list with no Fable, no
> Remote Control, no claude.ai connectors — and usage that no longer runs on the
> subscription you are paying for. Nothing on screen connects the two.
>
> That is why **Browser is the default** and why only the exact string `"token"`
> selects the other one. To undo it: delete `CLAUDE_CODE_OAUTH_TOKEN` from your
> user environment and restart the daemon.

**Moving an existing pane from Token to Browser is handled for you.** A pane
prepared for a forwarded token carries a Quil-written config saying its
first-run screens are answered — and the sign-in is one of those screens. When
the pane stops receiving a token, Quil removes just that answer so the sign-in
comes back. Your theme, trust answers and history are untouched. If a pane ever
does open on a prompt it cannot authenticate, `/login` inside the pane fixes it.

#### Browser — the default

Run `claude` in the pane and follow the prompt. If the browser callback cannot
reach the container, copy the code shown in the browser and paste it at the
`Paste code here if prompted` prompt.

Each pane has its own config directory, so this is **once per pane** — unless
you pick **Shared**, below.

#### Token — no per-pane sign-in

Pick **Token** on the Sign in row, or set `auth = "token"`, when the per-pane
sign-in is the bigger cost and the warning above is acceptable.

Open a sandbox pane and, if no token is
found, the pane signs itself in: it runs Anthropic's own `claude setup-token`
under a pseudo-terminal, opens your browser, and the pane shows

```
Signing in to Claude Code.
A browser window is opening — click Authorize there.
```

Click Authorize. Quil reads the token out of the command's output, saves it to
your **user environment**, and starts the container. Every later sandbox pane
is already signed in.

Opening a tab with two sandbox panes runs **one** sign-in, not two — a
daemon-wide single-flight guards it, and the pane that stood down is spawned by
the winner.

You can also do it ahead of time:

```bash
quil sandbox login     # the same flow, on demand
quil sandbox status    # where you stand
```

```
auth mode : browser (default)
token in this process : yes
token persisted       : yes
```

**Quil keeps no copy of the token.** On Windows it goes to `HKCU\Environment` —
the same place the System Properties dialog writes — so Quil is a UI over
somewhere you could have typed it yourself, not a credential store of its own.
Elsewhere Quil has nowhere it can reliably put it (a daemon started by launchd
or systemd reads no shell profile), so it prints the `export` line and lets you
place it.

Doing it entirely by hand is always an option: run `claude setup-token`, export
the result as `CLAUDE_CODE_OAUTH_TOKEN` where the **daemon** runs, and restart
the daemon.

Quil passes the variable *name* to Docker and lets Docker read the value from
its own environment, so the token never appears in a command line or in
`quild.log`.

> A plain pipe cannot drive `setup-token` — measured: with its output
> redirected the command prints nothing at all, opens the browser, and waits.
> That is why Quil runs it under a pseudo-terminal and mirrors it to your
> terminal, so the browser step stays visible and interactive.

#### Signing in once instead of once per pane

Pick **Shared** on the Sign in row. Every Shared pane mounts one config
directory, so the browser sign-in happens **once** rather than per pane, and
every pane still gets the full subscription.

It also merges those panes into **one trust domain**: any Shared pane can write
a hook or an MCP server that every other Shared pane's claude runs. Only the
panes that picked Shared are in it — a Browser pane keeps its own directory,
and a Codex or OpenCode container never gets the shared one. Off by default
(`shared_claude_config = true` makes Shared the default).

That is the trade to weigh against **Token** — one shared trust domain, or a
credential every Claude on the machine picks up.

The choice is recorded on the pane. Changing the default later does not move an
existing pane to the other directory, so its conversation still resumes.

#### What Quil never does

Quil never reads, copies, stores or refreshes a Claude credential in either
mode. Copying `~/.claude/.credentials.json` is **deliberately not
implemented** — it reads as "collect, store, or intermediate … session tokens"
under Anthropic's authentication policy, and it cannot work on a macOS host at
all (Keychain, not a file).

### Codex

**Nothing to do.** When a codex pane starts, Quil copies your host
`~/.codex/auth.json` (or `$CODEX_HOME/auth.json`) into the pane's own
`CODEX_HOME` inside the container. Codex runs against your own plan with no
sign-in and no device code.

It is a **copy, not a mount**, for two reasons: the container must not be able
to write back over the host's credential, and a bind-mounted file cannot be
replaced from inside when codex refreshes its token. Quil never overwrites an
existing one, and a missing host credential is not an error — the pane opens on
codex's own sign-in menu.

This is a **different posture from the Claude path, deliberately.** Anthropic
ships `claude setup-token` to mint a credential for exactly this purpose, and
its authentication policy speaks directly to intermediating credentials. Codex
ships no equivalent minting command, and copying the auth file is the mechanism
its own users use for containers.

Note that option 1 of codex's sign-in menu ("Sign in with ChatGPT") cannot work
from a container at all — the OAuth callback goes to a `localhost` the host
browser cannot reach.

### OpenCode

Sign in inside the container, with OpenCode's own flow. Its credentials live in
the container's home directory, which Quil does not mount, so this is **once
per container** — a pane restart (`Alt+R`) starts over.

---

## What the sandbox does and does not bound

**Bounded.** The agent's process reach is the container. Its filesystem reach is
the checkout (read-write), the repository's `.git`, and its own directory. Your
main checkout's working tree never enters the container. No other pane's data is
reachable. Your repository's git objects — every commit on every branch — cannot
be deleted from inside.

`.git` is mounted **read-write**, because git has to write the index, `HEAD` and
refs to let the agent commit at all. The pieces host git *executes* are pinned
read-only on top of it: `objects`, `hooks`, `config`, `config.worktree`,
`modules` and `worktrees`. `.git` is itself a **mountpoint**, and that is what
makes those pins mean anything — a mountpoint answers `EBUSY` to rename and to
remove, so the directory holding them cannot be moved aside and replaced.

**Not bounded.** Branch pointers: a sandbox pane can move or delete any branch
including `main`. That is recoverable — the objects survive, and `git reflog`
finds them — and making refs read-only would make committing impossible. The
Claude credential in the pane's own config directory, per Anthropic's own
warning about dev containers. Network access, which is the image's business.

---

## Things to know

- **Do not run `git gc`, `git repack` or `git commit-graph write`** inside a
  sandbox pane. New objects live in a per-pane store that Quil copies into your
  repository; repacking rearranges it underneath that.
- **File watchers need polling mode.** inotify events do not cross a Docker
  Desktop bind mount on Windows — measured. `tsc --watch`, `jest --watch`,
  `vite` and `nodemon` will sit silent and the agent will conclude its edits had
  no effect. Set `CHOKIDAR_USEPOLLING=1`, `--watch-poll` or the equivalent.
- **Bind-mount IO is roughly 20× slower.** Counting 1102 files: 0.14 s on the
  host, 4.5 s in the container. On a 100k-file monorepo, `git status`, ripgrep
  and a test run become minutes. A repository living inside WSL2 does not pay
  this cost.
- **Repositories with submodules are not supported.** A submodule's `.git` in a
  linked worktree is a relative path that escapes the worktree, and the mount
  layout does not preserve that offset.
- **Notifications, work state, input history and session resume all keep
  working.** Quil mounts a Linux `quild` into the container read-only and the
  agent's hooks call it, so a sandboxed pane shows the same spinner, the same
  green "needs you" mark and the same `Alt+Shift+I` history as a local one.
- **Usage limits.** Anthropic's documentation notes that advertised Pro and Max
  limits "assume ordinary, individual usage of Claude Code". A multiplexer makes
  many concurrent agents easy.

---

## If a repository starts erroring

Every git command in a repository failing with:

```
error: object directory …/sandbox/panes/<id>/objects does not exist;
       check .git/objects/info/alternates
```

means a sandbox pane's object store was removed while its reference remained.
Quil repairs this at daemon start — unless `$QUIL_HOME` itself was wiped
(`reset-daemon`), which also removes the record of where to look.

The manual fix is one line: open `<repo>/.git/objects/info/alternates` and
delete the line naming the missing directory. If it is the only line, delete the
file.

More sandbox symptoms: [Troubleshooting](troubleshooting.md#sandbox-panes).

---

## The sbx backend (Docker Sandboxes)

> **Status.** Creating, reusing and entering a sandbox, and sharing the
> worktrees folder, were run against a real `sbx` 0.48 on macOS. Attaching to
> an existing sandbox and every Windows path are covered by unit tests with a
> fake `sbx` only. `sbx` is young and its flags change between releases; check
> <https://docs.docker.com/ai/sandboxes/> if a command below is refused.

The Docker backend above runs the agent in a plain container. Quil builds that
cage itself, and it has no network limit. The **sbx backend** runs the agent in
a [Docker Sandboxes](https://docs.docker.com/ai/sandboxes/) microVM instead:

- each sandbox has its own kernel and its own Docker engine;
- only the workspaces you declare are shared with the host;
- outbound traffic goes through a host proxy with a **default-deny allowlist**.

Quil does not decide any of that. Your project declares it in an sbx
**environment file** and its **kits**, and Quil only points at them. Nothing
about your organisation's hosts, certificates or tools belongs in Quil.

### Install sbx (once per machine)

| Host | Install | Needs |
|---|---|---|
| macOS | `brew install docker/tap/sbx` | macOS 14+, Apple silicon |
| Windows | `winget install Docker.sbx` | Windows 11 x64, "Windows Hypervisor Platform" turned on |
| Linux | see the Docker Sandboxes docs | Ubuntu 24.04+, KVM, your user in the `kvm` group |

Then, once:

```sh
sbx login                    # sign in to Docker
sbx policy init balanced     # sbx refuses every create until a global policy exists
```

Quil checks both. When one is missing, the pane says which command to run.

### The profile: `.quil-sandbox.toml`

Put this file in your project. Quil looks for it in the pane's directory and
then in each parent directory, so a pane opened in a subdirectory finds it too.
Relative paths in it are relative to the file itself.

```toml
[sbx]
# Required. The sbx environment file (`sbx env create <file>`).
env_file = "sandbox/sbxenv.yaml"

# Optional. The sandbox name. Default: "quil-" + the folder name.
# Lower-case letters, digits, ".", "_" and "-".
name = "my-app"

# Optional. Runs ON THE HOST, in this folder, before the sandbox is created.
# A list of arguments, not a shell line: Quil runs no shell for you, so pick
# the interpreter yourself ("sh", "bash", "pwsh", ...). If it fails, the
# sandbox is not created and its output is shown in the pane.
prepare_host = ["sh", "sandbox/prepare-host.sh"]

# Optional. Each step runs INSIDE the sandbox (`sbx exec`) right after it is
# created.
post_create = [
  ["bash", "-c", "echo set-up done"],
]

# Optional. Passed to every post_create step as `sbx exec --env-file`.
post_create_env_file = "sandbox/setup.env"
```

**`sbx exec` does not get the environment file's `env:` block.** That block is
only for the agent's own session. So a `post_create` step does not see those
variables. Put the ones it needs in `post_create_env_file` (plain `KEY=value`
lines), or move the step into a kit's `setup.install`.

A key Quil does not know is an error, so a typo cannot quietly drop a step.

### Approve the profile: `quil sandbox trust`

The profile is in your repository, and the agent can write to your repository.
Without a check, an agent could change the host script and have Quil run it on
your machine, or widen its own cage through the environment file.

So Quil uses a profile only after you approve it:

```sh
quil sandbox trust            # in the project folder (or: quil sandbox trust <dir>)
```

It shows what will run on the host and every file the approval covers:

- the profile itself;
- the environment file;
- every file in each **local** kit the environment file names (a `kits:` entry,
  written as a string or with `source:`, that starts with `./` or `../`);
- `post_create_env_file`;
- each `prepare_host` / `post_create` argument that names a file (relative or
  absolute).

If **any** of these files changes, new sbx panes refuse to start until you run
`quil sandbox trust` again. Keep your kits under the environment file's folder
with a `./` path, so the approval covers them. `--yes` skips the question.
The approval is kept in `$QUIL_HOME/sandbox/sbx-trust.json`, on the machine
whose daemon runs the sandbox.

Rules for the covered files:

- **No symbolic links.** A covered file, a folder on its way from the profile's
  folder, and every file in a kit must be real files and folders. Quil cannot
  approve a link: the agent could change the file it points to. A profile with
  a link is refused; replace the link with the file itself.
- **`prepare_host` names one project file: its script.** Write it as a relative
  or an absolute path; both are covered. Name the program by its name
  (`sh`, found on `PATH`): an absolute command outside the profile's folder
  (`/bin/sh`) is refused, because Quil cannot tell it from a script in a folder
  the agent can write. A second project file (`["cp", "a.env", "b.env"]`) is refused:
  put that step inside the script.
- **The host step runs a copy.** Quil runs `prepare_host` with a private copy of
  the script you approved, not the file in the project, so a change after the
  check cannot run on your machine. The script runs in the profile's folder.
  It must find other files by paths relative to that folder, not through its
  own path (`$(dirname "$0")` points at the copy).
- **sbx creates the sandbox from a copy.** Quil gives `sbx env create` a private
  copy of the approved environment file, its local kits and
  `post_create_env_file`, in `$QUIL_HOME/sandbox/sbx-env/<name>/`, so a change
  in the project during the create does not count. In the copy, Quil writes the
  real folder in place of `${{ env.fileDir }}` and `${{ env.projectDir }}`, and
  makes a relative `workspace` or `additionalWorkspaces` path absolute. A
  workspace path that starts with another expression (`${{ env.args.x }}`) is
  refused: write it as an absolute path. Any other relative path in the
  environment file would point into the copy: use absolute paths there. An
  environment file Quil cannot read (bad YAML, a kit with no source, a YAML
  merge key `<<` or an alias `*name`) is refused.
- **No `lifecycle:` commands.** sbx runs them on your machine, and Quil cannot
  cover the scripts they call. An environment file with a `lifecycle:` block is
  refused; use `prepare_host` instead. Commands that a `secrets:` entry runs are
  covered as text only: keep their helper programs outside the project.
- In a worktree, `quil sandbox trust` approves the main checkout's profile, the
  one that worktree's panes use.

### Using it

In the `Ctrl+N` dialog, turn on the sandbox row with `Space`. When the host
has both Docker and sbx, `←`/`→` on that row switches between them. An sbx
pane has no image field and no sign-in row: both belong to your profile.
`←`/`→` also reaches the existing sandboxes that mount the pane folder (see
[Attach to a sandbox another tool owns](#attach-to-a-sandbox-another-tool-owns)).

`[sandbox] backend = "sbx"` in `config.toml` makes sbx the row's default.

From MCP: `create_pane` with `sandbox_backend = "sbx"` and no `sandbox_image`.
`list_plugins` reports `sbx_available`.

### What happens when a pane opens

1. Quil finds the profile and checks the approval. No profile or no approval:
   the pane shows why and does not start. **It never falls back to the host.**
2. If a sandbox with the profile's `name` exists, Quil uses it as it is: no
   host step, no create, no set-up step.
3. If not, Quil runs `prepare_host`, then
   `sbx env create <env_file> --name <name>` with the flag that applies the plan without asking (`--force` on sbx 0.48, `--auto-approve` on 0.47; Quil reads which from `sbx env create --help`), then each
   `post_create` step. All output is shown in the pane.
4. If your project has a worktrees folder (below), Quil shares it with the
   sandbox: `sbx mount <name> <folder>`.
5. The pane runs its agent with
   `sbx exec -it -w <pane folder> <name> <agent command>`.

Quil uses `sbx exec`, not `sbx run`, because `exec` runs the pane's own agent
command with the pane's own options, and several panes can share one sandbox.
This means the environment file must mount your project at the **same path** as
on the host (the sbx convention). On a Windows host Quil leaves out `-w`, and
the agent starts in the sandbox's main workspace.

**Closing a pane stops its agent inside the sandbox.** Ending the
`sbx exec` process on your machine does not end the program it started inside
the VM, so Quil starts each agent with a tag (`QUIL_SBX_PANE`) and, when the
pane closes, restarts or is replaced, stops only the processes with that tag.
Each start gets a new tag, so a restart never stops the new agent. If the
daemon was killed and could not do this, the pane stops its old agents the
next time it starts.

**Closing a pane does not remove the sandbox.** Many panes can use it, and it
keeps what the set-up steps installed. To build it again (for example after you
changed the kits):

```sh
sbx rm --force my-app
```

The next sbx pane then creates it again.

### Worktrees

Quil puts every worktree of a repository in one folder next to it:
`<repo>-worktrees/<branch>`. Your environment file shares only the repository,
so Quil shares that folder too. You do not change your environment file.

- **One sandbox for the repository and all its worktrees.** A pane in a
  worktree uses the profile of the main checkout, so it runs in the same
  sandbox, with the same sign-in and the same approval. (A worktree's own copy
  of the profile is used only when the main checkout has none.)
- **Before each pane starts**, Quil runs `sbx mount <name> <repo>-worktrees`.
  sbx adds the folder to the running sandbox. It does not restart it, and the
  panes already running in it keep running. sbx keeps the folder after a stop
  and start, and a repeated mount does nothing.
- **A worktree you make later is visible at once**, because the whole folder
  is shared, not each worktree.
- Quil shares the folder only when it exists and is a real folder, not a
  symbolic link.
- Quil shares only the worktrees folder of a repository **inside the profile's
  folder**. A worktree counts only when the main repository lists it in its
  own `.git/worktrees/`, so `.git` files written in the project cannot make
  Quil share another repository. Quil compares real paths, after
  following every link, and shares the real folder.
- If the mount fails, a pane in a worktree does not start and shows sbx's
  error. A pane in the main checkout starts anyway, with a note.

Git works in a worktree inside the sandbox because the worktree points to the
main checkout's `.git`, which your environment file already shares.

### Attach to a sandbox another tool owns

Sometimes another tool creates and removes your sandboxes, for example one
sandbox per task. Quil can run panes in such a sandbox without a profile.

**How to use it.** In the `Ctrl+N` dialog, pick the pane folder first. Then turn
on the sandbox row and press `←`/`→` until the row says `attach: <name>`. Quil
gets the list from `sbx ls --json` each time the dialog opens. The sandboxes
that sbx lists with the pane folder come first. The others come after them,
marked `folder checked at start`, because `sbx ls` does not show a folder that
was added later with `sbx mount`.

From MCP: `create_pane` with `sandbox_backend = "sbx"` and
`sandbox_sbx_name = "<name>"`.

**What Quil does:**

1. Runs `sbx ls --json`. If no sandbox has that name, the pane does not start.
   Quil never creates it, so create it with the tool that owns it.
2. Checks that the pane folder is in a writable folder of the sandbox. The most
   specific folder decides: a read-only folder inside a writable one stays
   read-only. Quil compares real paths, after it follows every link.
3. If `sbx ls` does not show the folder as writable, Quil asks the sandbox
   itself: it runs a short `test -d` and `test -w` on the folder inside the
   sandbox. If that also says no, the pane does not start.
   On a Windows host Quil skips this step, because a Windows path means
   nothing inside the sandbox. The pane starts with a note, and the agent
   starts in the sandbox's main workspace, as every sbx pane on Windows does.
4. Starts the agent with `sbx exec -it -w <pane folder> <name> <agent command>`.
   If the sandbox is stopped, `sbx exec` starts it.

**What Quil does not do:** it needs no profile and no `quil sandbox trust`. It
runs no host step and no set-up step. It does not `sbx mount` anything, does
not change the sandbox, and does not remove it. Closing the pane stops only that
pane's own agent. The pane keeps the sandbox name, so a restart and a restored
workspace go back to the same sandbox.

**The repository cannot choose the sandbox.** You pick it in the dialog, and
the name is saved with the pane. No file in the project can send the pane to a
different sandbox.

**Sign-in belongs to the sandbox.** The agent uses whatever login is inside the
sandbox. If the tool that owns the sandbox keeps the login up to date, that
tool's own connect step does it, not Quil.

### Pasting a screenshot

Paste an image (Ctrl+V, Ctrl+Alt+V or F8) into an sbx pane as into any other
pane. Quil copies the image into the pane's sandbox, as
`/tmp/quil-paste/quil-paste-<time>-<random>.png`, and types that path. The agent
reads it there. Only that one image goes into that one sandbox; nothing is
saved on your machine for it.

The copy makes the file readable by every user inside the VM (`sbx cp` leaves
it owned by root). It needs the agent to be running; a paste into an exited
pane says so in the status bar.

### What an sbx pane does not have (yet)

- **No notifications, work spinner, input history or session resume.** Those
  need Quil's hook program inside the cage, and the cage's mounts belong to
  your environment file. The agent starts a fresh session on every spawn.

### Example: a minimal environment file and kit

Generic example. Allow only Anthropic's hosts, plus a tunnel to a database on
the host at `localhost:5432`.

`sandbox/sbxenv.yaml`:

```yaml
schemaVersion: "1"
agent: claude
workspace: /home/me/projects/my-app      # mounted at the same path inside
kits:
  - source: ./kits/networking            # local kits need ./ or ../
env:
  APP_ENV: sandbox
```

`sandbox/kits/networking/spec.yaml`:

```yaml
schemaVersion: "2"
kind: mixin
name: example-networking
version: "1.0.0"
permissions:
  network:
    allow:
      - api.anthropic.com
      - platform.claude.com      # Claude Code sign-in refresh
      - console.anthropic.com    # older Claude Code sign-in refresh
      - localhost:5432           # the tunnel below
setup:
  install:
    - description: "Install socat"
      user: root
      command: |
        apt-get update -qq && apt-get install -y --no-install-recommends socat
  startup:
    - command: [sh, -c, "nohup socat TCP-LISTEN:5432,bind=127.0.0.1,reuseaddr,fork TCP:host.docker.internal:5432 >/dev/null 2>&1 &"]
```

Files under a kit's `files/home/...` are copied into `/home/agent/...` in the
sandbox.

Common sbx errors and their fix:

| Message | Fix |
|---|---|
| `Not authenticated to Docker` | `sbx login` |
| `global network policy has not been initialized` | `sbx policy init balanced` |
| `resolve kits: ... invalid reference` | start the kit path with `./` or `../` |
