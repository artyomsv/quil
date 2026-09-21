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
5. Type the image name, or leave the pre-filled one from
   `[sandbox] default_image`.
6. **Claude Code only:** pick **Sign in** — `Browser` (default) or `Token`. See
   [Signing in](#signing-in).
7. Press **Continue**.

Closing the pane removes its container. Restarting the pane (`Alt+R`) builds a
new one.

### Which agents can be sandboxed

| Plugin | Sandbox | Sign-in inside the container |
|---|---|---|
| **Claude Code** | Yes | Browser once per pane (default), or a forwarded token — you choose per pane |
| **Codex** | Yes | None. Quil copies your host `~/.codex/auth.json` into the pane |
| **OpenCode** | Yes | Once per container, in the container |
| Terminal, lazygit, k9s, … | No | The row is not offered — these are not AI panes |

---

## Building the image

```bash
scripts/sandbox-image.sh
```

That builds `quil-sandbox:latest` on your machine from
`docker/sandbox/Dockerfile`, then **verifies the result** — it asks the image
for a non-root user, a working `claude`, and `git`, rather than reporting
success from a clean build log.

Point the dialog at it once:

```toml
[sandbox]
default_image = "quil-sandbox:latest"
```

### Flags

| Flag | Effect |
|---|---|
| `--tag NAME:TAG` | Build under a different tag. Default `quil-sandbox:latest` |
| `--base IMAGE` | Different base image. Default `node:22-bookworm-slim` |
| `--claude-version V` | Pin the Claude Code npm version. Default `latest` |
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

Two flows, both from Anthropic's dev-container documentation. **Pick one per
pane** on the **Sign in** row of the create dialog, or set the default in
config:

```toml
[sandbox]
auth = "browser"    # or "token"
```

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

#### Browser — the default

Run `claude` in the pane and follow the prompt. If the browser callback cannot
reach the container, copy the code shown in the browser and paste it at the
`Paste code here if prompted` prompt.

Each pane has its own config directory, so this is **once per pane** — unless
you set `shared_claude_config`, below.

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

`shared_claude_config = true` gives every sandbox pane one config directory, so
the browser sign-in happens **once ever** rather than per pane, and every pane
still gets the full subscription. It also merges them into **one trust domain**:
any sandbox pane can then write a hook or an MCP server that every other sandbox
pane's claude runs. Off by default.

That is the trade to weigh against `auth = "token"` — one shared trust domain,
or a credential every Claude on the machine picks up.

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
