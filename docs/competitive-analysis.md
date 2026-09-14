# Competitive Analysis — herdr, Agent of Empires & the Ghostty axis

> Deep comparison of Quil against the two closest direct competitors in the
> "terminal multiplexer for AI coding agents" category, captured 2026-07-06.
> Licence and repository-ownership rows re-verified 2026-08-20.
> **herdr rows and every Quil row re-verified 2026-09-10** against herdr.dev/docs;
> the aoe column still reflects the original 2026-07-06 capture and is the next
> thing to re-check. Four herdr claims had gone stale by then and are corrected
> below: Windows is no longer beta, one herdr client now holds several machines
> at once, agent breadth is 24+ rather than 20+, and detection is a two-layer
> system (installed lifecycle hooks are authoritative, screen manifests are the
> fallback) rather than screen heuristics alone.
> **Ghostty added 2026-09-10** in its own section — not a rival, but the emulator
> Quil is drawn into, and the origin of the one credible future rival.
> Feeds the [competitive-gap section of the roadmap](roadmap.md#planned--competitive-gaps-herdr-aoe).

The classic-multiplexer comparisons (tmux, zellij, WezTerm, screen, Ghostty) live
on the marketing site under `/vs/*`. This document covers the two products that
share Quil's actual thesis — *persistent, agent-aware multiplexing* — and are
therefore the more honest mirror of where Quil leads and where it trails, plus a
section on Ghostty, which is neither.

- **herdr** — <https://github.com/herdrdev/herdr> (Rust, Apache-2.0)
- **Agent of Empires (aoe)** — <https://github.com/agent-of-empires/agent-of-empires> (Rust + React, MIT, Mozilla.ai-backed)

---

## TL;DR positioning

All three are "tmux for AI coding agents": persistent multiplexers that run many
agents in parallel and roll their status up to a glance. Each made a different
architectural bet.

| | **Quil** | **herdr** | **Agent of Empires** |
|---|---|---|---|
| Language | Go | Rust | Rust + React/TS |
| Multiplexer substrate | Own daemon + own PTY | Own server + own PTY (vendors libghostty-vt) | **Wraps tmux** (hard dependency) |
| VT emulation | `charmbracelet/x/vt` | Vendored libghostty-vt (Ghostty engine) | `vt100` crate over `tmux pipe-pane` |
| Client/server | daemon + TUI client | server + thin client(s) | tmux + TUI + optional HTTP daemon |
| **Windows** | ✅ **Native** (bundled ConPTY/OpenConsole) | ✅ Native, **GA** (ConPTY) as of 2026-09-10 — no terminal attach, no live handoff, no clipboard image bridge | ❌ **WSL2 only** |
| Agent-drives-it API | **MCP server** (35 tools, native protocol) | Socket API + full CLI + agent skill | HTTP REST API (130 routes) + CLI |
| Web/browser UI | ❌ TUI only | ❌ (responsive TUI) | ✅ **React PWA dashboard** |
| Container sandbox | ✅ **Docker** (per-pane, user-supplied image) | ❌ | ✅ Docker/Podman/Apple |
| Remote phone access | ❌ | via SSH TUI | ✅ Tunnel + PWA + Web Push |
| Git worktree-per-session | ✅ | ✅ | ✅ (+ multi-repo) |
| AI agents supported | **3** deep + tools | **24+** detected, one-command integrations for 18 | **~13** terminal, 7 ACP |
| Several machines in one client window | ✅ (v1.47, per-host reconnect) | ✅ (sidebar machine switch, per-host fault isolation) | ❌ |
| Scale | ~30–40k Go | ~170k Rust | ~292k Rust + large web app |
| License / backing | Apache-2.0, product | Apache-2.0 (was AGPL-3.0 + commercial until 2026-07-22), solo + sponsors | MIT, Mozilla.ai community |

**On herdr's licensing, because the reversal is the interesting part:** it
shipped in March 2026 dual-licensed AGPL-3.0-or-later plus a commercial licence
sold by direct contact — the model that exists to stop a company using your work
without contributing back. It ran that for four months and dropped it on
2026-07-22 for plain Apache-2.0, with no commercial tier now offered anywhere
public. The repo also moved from a personal account to the `herdrdev` org.

What they kept is more telling than what they dropped. herdr has never had a CLA
or DCO; instead `.github/APPROVED_CONTRIBUTORS` is a bot-enforced allowlist, and
CONTRIBUTING states that unsolicited pull requests are closed automatically
"regardless of their size, title, test results, or whether a human or an agent
wrote the code." That allowlist predates the relicense by two months and is
still in use after it. So the protection they actually wanted came from
controlling who contributes, not from the licence — worth remembering before
treating a copyleft-plus-commercial split as the answer to the same worry.

**The blunt summary:** herdr is Quil's closest philosophical twin (own
multiplexer, single binary, socket API, native-Windows ambition) but far ahead on
agent breadth, plugins, and remote. aoe leans on tmux and pours its energy into a
web/mobile dashboard, an ACP "structured view", and container sandboxing across
three runtimes — Quil now has Docker sandbox panes of its own, but the browser
and phone surfaces remain missing. Both competitors are considerably larger and
support many more agents. Quil's real moats are **native Windows maturity**, being
a **first-class MCP server**, and two unique niceties (**pane notes**,
**memory reporting**).

---

## Capability matrix

Legend: ✅ full · 🟡 partial/different · ❌ absent · ❓ not re-verified

### Core multiplexer

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Panes / splits (mixed H/V) | ✅ | 🟡 (tmux) | ✅ |
| Tabs | ✅ | 🟡 (tmux windows) | ✅ |
| Workspaces (top-level project container) | ✅ | ✅ (profiles/groups) | ✅ (projects, v1.47 — each project is host-scoped; see the multi-host row) |
| Zoom / focus single pane | ✅ | ✅ | ✅ (Ctrl+E) |
| Move pane across tabs without killing process | ✅ | 🟡 | ❌ |
| Mouse-native (drag borders, click, reorder) | ✅ | 🟡 (web) | ✅ |
| Scrollback + scrollbar drag | ✅ | ✅ | ✅ |
| Text selection (kbd + mouse) | ✅ | ✅ | ✅ |
| Vim-style copy mode | ✅ | 🟡 | ❌ (selection only) |
| Portable layout export/apply | ✅ | ❌ | ❌ |
| Wide-canvas / native-vs-preview AI pane rendering | ❌ | 🟡 (structured) | ✅ |

### Persistence & remote

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Detach/reattach, survives disconnect | ✅ | ✅ (tmux) | ✅ |
| Snapshot restore after server restart | ✅ | ✅ | ✅ (ghost buffers) |
| Survives full host reboot | ✅ | ✅ (tmux) | ✅ |
| Named sessions (separate server namespaces) | ✅ | 🟡 (profiles) | ❌ |
| Remote attach over SSH (thin client) | ✅ | 🟡 (SSH+web) | ✅ (v1.44; several hosts at once since v1.47) |
| Several machines live in one client at once | ✅ (each keeps its own server; only the selected one streams pane screens, the rest keep reporting workspace + agent state; a lost host does not drop the others) | ❌ | ✅ (v1.47; per-project host tag, per-host reconnect state) |
| Approval-gated auto-install on a bare remote | ✅ | ❌ | ✅ (`internal/remoteinstall`) |
| Clipboard-image bridged into remote session | ✅ | ✅ (web) | ❌ |
| Live server handoff (upgrade without killing panes) | ✅ | 🟡 (detached workers) | ❌ |
| Web dashboard (browser terminal) | ❌ | ✅ (PWA) | ❌ |
| Remote phone access (tunnel + QR/passphrase) | ❌ | ✅ | ❌ |
| Web Push / mobile notifications | ❌ | ✅ (VAPID) | ❌ |

### Agent awareness

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Status rollup (blocked/working/done/idle) | ✅ | ✅ | ✅ |
| Screen-content heuristic detection (no hooks) | ✅ (TOML manifests over screen + title + OSC progress) | ✅ | 🟡 (idle patterns only) |
| Hook/integration-based state | ✅ (authoritative; suppresses the manifest) | ✅ | ✅ (claude, opencode, codex) |
| Detection rules editable on disk, no rebuild | ✅ (`~/.config/herdr/agent-detection/<agent>.toml` overrides the bundled manifest) | ❓ | ✅ (embedded defaults are *copied out* to the plugins dir by `internal/plugin/defaults.go`; `LoadFromDir` reads any `.toml` there, and the registry hot-reloads) |
| Detection rules that **refresh themselves from a URL** | ✅ (auto fetch from herdr.dev, `server update-agent-manifests`) | ❌ | ❌ **— this, and only this, is the real gap** |
| Brand-new agent addable without a new binary | ❌ (process detection is compiled in) | ❓ | ✅ (a plugin TOML defines the spawn command and its idle handlers; no Go change) |
| Detection debugger (`agent explain`) | ✅ | ❌ | ❌ |
| Model/context-token status display | 🟡 | ✅ | ✅ |
| AI agents with detection | 24+ | ~13 | **3** |
| One-command agent integration installer | ✅ (`herdr integration install <agent>`, 18 agents) | ✅ | ❌ |
| Native agent session restore | ✅ (most of its integrations) | ✅ (Claude+) | ✅ (claude, opencode, codex) |
| Session fork | ❌ | ✅ | ❌ |
| Session import from disk | ❌ | ✅ (Claude) | ❌ |

### Git / repos

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Worktree-per-session (auto branch + worktree) | ✅ | ✅ | ✅ (tab opens onto a new worktree; close offers removal) |
| Multi-repo workspace (one session, N repos) | ❌ | ✅ | ✅ (projects, v1.47 — each owns one root dir; a client shows projects from several hosts, but one project belongs to exactly one host) |
| Built-in diff viewer (review + edit) | ❌ | ✅ | ❌ |
| Inline diff comments → prompt to agent | ❌ | ✅ | ❌ |
| Lazygit / git-tool integration | 🟡 (plugin) | ✅ (tool sessions) | ✅ (Alt+G overlay) |

### Sandboxing / isolation

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Docker container sandbox | ❌ | ✅ | ✅ |
| Podman / Apple Containers | ❌ | ✅ | ❌ (untested) |
| Shared auth volumes (in-container login) | ❌ | ✅ | 🟡 opt-in — per-pane by default, since one shared config directory is one trust domain |

### Extensibility

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Declarative pane-type plugins (TOML) | 🟡 | 🟡 | ✅ |
| Executable plugins (any language) | ✅ | ✅ (design) | ❌ |
| Plugin actions / event hooks / link handlers | ✅ | ✅ | ❌ |
| Plugin marketplace (GitHub topic index) | ✅ | ✅ (featured + hash) | ❌ |
| Agent-drives-multiplexer API | ✅ socket+CLI | ✅ HTTP+CLI | ✅ **MCP (35 tools)** |
| Subscribable event stream (`events.subscribe`) | ✅ (workspace/tab/pane/layout/worktree lifecycle) | 🟡 | 🟡 (`watch_notifications` + task completion only) |
| Wait on *semantic* agent state (`--until done/blocked`) | ✅ | 🟡 | ✅ (`wait_task`, daemon-side ledger) |
| Portable layout export / declarative apply | ✅ (`layout.*`) | ❌ | ❌ (layout persists, but is not exportable) |
| General CLI to script panes | ✅ | ✅ | ❌ |
| MCP server forwarding to agents | ❌ | ✅ | ❌ |
| ACP structured view (plan/tool/approve cards) | ❌ | ✅ | ❌ |
| Command palette | 🟡 | ✅ (web) | ✅ (M11 — Alt+Shift+P, fuzzy actions + unified scrollback search) |

### Config / UX / ops

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Themes (multiple, light/dark auto) | ✅ (18) | ✅ (8) | 🟡 minimal |
| Sound notifications | ✅ | ✅ | ❌ |
| OS/terminal desktop notifications | ✅ | ✅ (push) | 🟡 Windows toasts + click-to-route; no macOS/Linux |
| In-TUI notification center | 🟡 | ✅ | ✅ |
| Repo config + lifecycle hooks | 🟡 | ✅ | ❌ |
| Profiles (per-project workspaces) | 🟡 | ✅ | 🟡 (projects, v1.47 — a project scopes tabs and a root directory, not a saved profile) |
| Auto-stop idle sessions | 🟡 | ✅ | ❌ |
| Groups / favorites / snooze / archive / trash | 🟡 | ✅ | ❌ |
| Self-update (in-app) | ✅ | ✅ | ✅ (check + stage in the background, prompt in the TUI, rename-aside swap with rollback applied at next launch) |
| Self-update as a *CLI subcommand* | ✅ | ✅ | ❌ (there is no `quil update`; the CLI switch is daemon/mcp/notify/sandbox/version/remote/restart/status) |
| Pane notes (per-pane editor) | ❌ | ❌ | ✅ |
| Memory reporting (heap + PTY RSS) | ❌ | ❌ | ✅ |
| Windows clipboard image-paste proxy | ⚠️ unverified | ❌ | ✅ |
| Input / command history per pane | 🟡 | ✅ | ✅ |

---

## The 20 features worth chasing

The most interesting capabilities Quil lacks or only partially supports, ranked
by a blend of strategic impact and how differentiating they are. Effort is a rough
T-shirt size. "Maps to" links a gap to an existing roadmap item it extends.

The ranking is kept as it was first written — re-scoring it every release would
destroy the record of what looked most urgent at the time, which is the only
reason a list like this is worth keeping. Rows that have since shipped are struck
through and marked instead, so the table stays readable as history *and* as a
current to-do list.

| # | Feature | Source | Why it matters | Effort | Impact | Maps to |
|---|---|---|---|:---:|:---:|---|
| 1 | Screen-content agent state detection (no hooks) | herdr, aoe | Blocked/working/done inferred from terminal output for *any* agent, zero hooks. Quil only pattern-matches idle. | M | ★★★ | process-health |
| 2 | Broad agent support + detection registry | herdr, aoe | They detect 13–24+ agents (Codex, Gemini, Cursor, Copilot, Droid, Devin…); Quil ships 3. Starkest gap, and the one still widening — herdr was 20+ at the 2026-07-06 capture and 24+ by 2026-09-10. | M | ★★★ | community-plugins |
| 3 | ~~Git worktree-per-session~~ **SHIPPED** | herdr, aoe | Auto branch + worktree on session create, cleanup on delete. The #1 adoption driver for these tools. A tab can open onto a new worktree (placeholder pane + spinner while `git worktree add` runs), and closing it offers to remove the worktree, naming what it holds. | M | ★★★ | workspace-files |
| 4 | Built-in diff viewer (review + edit + commit) | aoe | Review agent changes without leaving the TUI. Table stakes for "review what the agent did". | M | ★★★ | new |
| 5 | Executable/scriptable plugins (actions, event hooks, link handlers) | herdr, aoe | Any-language plugins that run logic, not just declare pane types. Unlocks a real ecosystem. | M | ★★★ | community-plugins, cross-pane-events |
| 6 | Plugin marketplace (GitHub-topic index) | herdr, aoe | Discover + `install owner/repo`. Already partially planned. | M | ★★ | community-plugins |
| 7 | General shell CLI to script the multiplexer | herdr, aoe | `quil pane split`, `quil tab create` from any script. MCP serves AI; humans/scripts have nothing. | M | ★★ | new |
| 8 | ~~Remote SSH thin-client attach (`--remote`)~~ **SHIPPED (v1.44)** | herdr | Local client of a remote server. Since v1.47 one client holds the local daemon and any number of remote ones at once, which is more than the gap asked for. The clipboard-image half of this gap did **not** ship: the paste proxy writes the PNG to the client's own disk and types that path into the PTY, so in remote mode it names a file the server cannot read. | M | ★★ | session-sharing |
| 9 | Web dashboard (browser terminal) | aoe | Real terminal + diffs in the browser, installable PWA. The largest surface Quil is missing. | L | ★★★ | new |
| 10 | Remote phone access (tunnel + QR/passphrase + push) | aoe | Check on agents from a phone via Tailscale/Cloudflare with two-factor pairing. | L | ★★ | session-sharing |
| ~~11~~ | ~~Container sandboxing (Docker/Podman) + shared auth volumes~~ **SHIPPED** | aoe | Docker only; Podman untested. Per-pane container, user-supplied image, the mount set as the boundary. Auth is per-pane rather than a shared volume — a shared Claude config directory is available but off by default, because it merges every sandbox pane into one trust domain. | L | ★★ | [sandbox-panes](sandbox-panes.md) |
| 12 | ~~Multi-repo workspaces~~ **SHIPPED (v1.47)** | aoe | One session/branch spanning several repos. Quil's projects each own a root directory and their own tabs — and a project can belong to a different machine, which aoe's workspaces do not span. | M | ★★ | workspace-files |
| 13 | Inline diff comments → prompt to agent | aoe | Annotate a diff; comments assemble into one prompt back to the agent. Tight review loop. | M | ★★ | (extends #4) |
| 14 | Sound notifications | herdr, aoe | Audible cue when an agent needs you. Cheap, immediately felt. | S | ★★ | notification-center |
| 15 | ~~OS/desktop notifications (beyond in-TUI sidebar)~~ **PARTLY SHIPPED** | herdr, aoe | OSC/`notify-send`/`terminal-notifier` so alerts leave the TUI (works over SSH). Windows toasts land, and a click routes to the pane that raised it. macOS and Linux are still open — no transport there carries a click back to a pane. | S | ★★ | notification-center |
| 16 | One-command agent integration installer | herdr, aoe | `quil integration install <agent>` writes the agent's hooks for you. | M | ★★ | (extends #1/#2) |
| 17 | Themes + light/dark auto-switch | herdr, aoe | 8–18 presets, follows host OSC 10/11. Quil's theming is minimal. | S–M | ★★ | new |
| 18 | Session fork | aoe | Branch a conversation into a new independent session, parent untouched. | M | ★★ | new |
| 19 | Repo config + lifecycle hooks (`on_create`/`on_launch`/`on_destroy`) | aoe | Per-project `.quil.toml` hooks. Natural extension of workspace files. | M | ★★ | workspace-files |
| 20 | Session lifecycle management (auto-stop idle, groups/favorites/snooze/archive) | aoe | Keep a large fleet tidy: reap idle sessions, organize + archive them. | M | ★★ | new |

Runners-up considered but cut from the top 20: named sessions, live server
handoff, ACP structured view (novel but very large), `agent explain` detection
debugger, MCP-config forwarding to agents, smart auto-rename, self-update command,
vim-style copy mode, agent command overrides.

---

## Addendum — borrow candidates found in the 2026-09-10 herdr re-read

The top-20 above is frozen as written. These are the ideas the September re-read
surfaced that the July capture missed, kept separate so the original ranking
stays a record rather than a moving target. They are ordered by ROI against
Quil's *existing* architecture — several are small because Quil already has the
hard part built and is missing only the seam.

| # | Borrow | Why it fits Quil specifically | Effort |
|---|---|---|:---:|
| A1 | **Detection manifests that refresh themselves from a URL** | Narrower than it first looks, and worth stating precisely so it is not over-built. Quil is *not* missing on-disk customisation: `internal/plugin/defaults.go` copies the embedded TOML out to the plugins dir, `Registry.LoadFromDir` reads any `.toml` sitting there, and the registry hot-reloads — so a user can already override an idle handler, or add a brand-new agent as a pane type, with no rebuild. herdr cannot do that last part at all (its process detection is compiled in). **The one thing herdr has that Quil does not is self-refresh**: its manifests auto-fetch from herdr.dev, so an agent whose prompt string changed is fixed for every user without anyone editing a file. That is the whole gap — a signed fetch into the plugins dir plus a "don't clobber my edits" rule, not a new plugin format. | S–M |
| A2 | **`pane.read` with a `source` selector, incl. `detection`** | Quil splits this across `read_pane_output` (ANSI-stripped scrollback) and `screenshot_pane` (VT-emulated screen). herdr has one call with `visible` / `recent` / `recent-unwrapped` / `detection`. Two parts are worth taking: `recent-unwrapped`, which pulls real scrollback out of a full-screen agent that owns the alternate screen — exactly the case Quil's `screenshot_pane` handles worst — and `detection`, which returns *why* the state was classified as it was. | S–M |
| A3 | **`agent explain` — a detection debugger** | A runner-up in July; A1 promotes it to a prerequisite. The moment detection rules are data the user can override, "it says idle and it is not" becomes unanswerable without a command that prints which manifest, which pattern and which screen rows produced the verdict. Do not ship A1 without it. | S |
| A4 | **Sidebar row templating + agent-pushed metadata** | herdr's sidebar rows are token templates (`state_icon`, `workspace`, `agent`, custom `$name`), with `rows_by_agent` overrides and conditional rules on text match or numeric compare — and agents fill those custom tokens by calling `pane.report_metadata` with display labels and token maps. Quil's sidebar is fixed, but Quil has the better delivery path: an MCP `report_pane_metadata` tool lets an agent label its own pane ("reviewing PR #213", "82% context") with no plugin at all. Highest ratio of visible payoff to code in this table. | S–M |
| A5 | **`notification.show` — let an agent raise a notification** | Quil's notification tools are all consumer-side: `get_notifications`, `watch_notifications`, `dismiss_notifications`. There is no way for an agent to *raise* one. Quil already has the whole delivery chain built — event queue, sidebar, Windows toast with click-to-route — so this is a tool definition and a message type over machinery that exists. | S |
| A6 | **General event subscription over MCP** | herdr's `events.subscribe` pushes workspace / tab / pane / layout / worktree lifecycle events on a long-lived stream. Quil's `watch_notifications` blocks on sidebar-worthy events only. The daemon already has a watcher pub/sub (`internal/daemon/event.go`) feeding exactly that tool — widening it to pane and layout events is mostly a matter of what gets published. | M |
| A7 | **`pane.wait-output` — wait for text or a regex** | Quil can wait on *semantic* agent state (`wait_task`) and on shell command completion (OSC 133), but has no primitive for "block until this pane prints X". That is the one an agent driving a terminal pane reaches for most. | S |
| A8 | **Per-agent notification sound and muting** | Cheap and immediately felt (this is top-20 #14, sharpened). herdr plays MP3s with separate `done_path` / `request_path` and lets you mute one noisy agent while keeping the rest. Quil already mutes per pane; per-*agent-type* is the axis it lacks. | S |
| A9 | **Marketplace as a GitHub-topic index** | Top-20 #6, with herdr's implementation detail that removes the objection: no hosting, no submission, no review queue — auto-index public repos carrying the topic `herdr-plugin` that contain a manifest, refreshed every 30 minutes. Quil could index `quil-plugin` the same way and the whole "marketplace" is a static JSON file a scheduled job writes. | S–M |
| A10 | **Plugin pane placement modes** | herdr plugin panes declare overlay / popup / split / tab / zoomed. Quil has exactly one shared overlay slot, which is why lazygit, hunk, k9s and lazysql all contend for it. Widening the placement vocabulary is the unlock for more than one overlay-shaped tool at a time. | M |
| A11 | **Portable layout export / declarative apply** | `layout.*` exports a tab tree and applies one back. Quil already serialises its `LayoutNode` tree to JSON for persistence — the data is there, the seam is not. Makes "give me my standard 4-pane review layout" a one-liner. | S–M |
| A12 | **Ctrl+click link handlers routed to a plugin** | A regex on a terminal URL routes the click to a plugin action instead of the browser. Turns "the agent printed a PR link" into "open it in the lazygit pane". Depends on executable plugins (top-20 #5). | M |

**The strategic signal, separate from any feature:** herdr has announced *Herdr
Cloud* — SSH-free machine connectivity, waitlist open — on the back of seed
funding, and reports ~1,000 community plugins. Both point the same way: the
competition for this category is shifting from "which multiplexer is better" to
"whose network of machines and plugins is easier to join." Quil's answer to the
first half already exists and is arguably better positioned (`--remote` opens no
port and needs no vendor in the path); the second half is A9.

---

## The Ghostty axis — the emulator underneath, and Superlogical

> Captured 2026-09-10. Ghostty's newest release is **1.3.1 (2026-03-13)**; 1.4.0
> is in progress. Ghostty is Zig, MIT, ~60.9k stars, and ships for **macOS and
> Linux only** — there is no official Windows build, only community forks and a
> rename-forced derivative called Noctty.

[Ghostty](https://ghostty.org) is not a competitor. It is a terminal *emulator* —
the window Quil is drawn into. Quil runs inside Ghostty exactly as it runs inside
WezTerm, Windows Terminal or iTerm2, and the site's WezTerm page already makes
that argument. Three things make it worth a section anyway.

**1. Ghostty's author is now building a multiplexer.** On 2026-07-30 Mitchell
Hashimoto announced [Superlogical](https://mitchellh.com/writing/superlogical), a
company whose first product is a terminal multiplexer built on libghostty:
server-side sessions, clients that reconnect from the web and from native macOS
and iOS apps, live session sharing, and — per the launch coverage — human work
and AI agents together in long-running sessions. That is Quil's thesis, funded,
from the person who wrote the fastest terminal emulator in the field. Nothing has
shipped and no timeline is public; the announcement deliberately withholds
features, architecture and dates. Treat it as the most credible *future*
competitor rather than a present one, and re-check it every release.

**2. The emulator is growing multiplexer-shaped features.** Ghostty 1.3.0
(2026-03-09) added scrollback search, native scrollbars, editable tab titles,
split drag-and-drop, split zoom preservation, and separate working-directory
inheritance for windows, tabs and splits. tmux control mode is an open request
([#1935](https://github.com/ghostty-org/ghostty/issues/1935)) that Hashimoto has
publicly framed as a step toward a libghostty-based tmux replacement. An emulator
that renders a remote multiplexer's panes as native tabs takes the low end of
multiplexing away from tmux — and from anyone whose pitch is only "tabs and
splits". Quil's pitch is not, which is exactly why the distinction has to stay
sharp on the marketing site.

**3. libghostty-vt is the VT engine our closest rival already vendors.** It is
MIT, zero-dependency, lifted from Ghostty's production core, and builds for
macOS, Linux, Windows and WebAssembly. herdr vendors it; Quil uses
`charmbracelet/x/vt`. Adopting it would mean cgo, and cgo would cost Quil the
pure-Go cross-compile to five platforms and complicate the native-Windows story
that is its strongest wedge. **Recommendation: do not adopt it.** Revisit only if
`charmbracelet/x/vt` becomes a correctness problem we end up fixing ourselves.

### What is worth borrowing from Ghostty

Ranked. None of these is a multiplexer feature — they are terminal-craft features
Quil is well placed to take.

| # | Idea | Ghostty | Why Quil should care | Effort |
|---|---|---|---|:---:|
| G1 | **Use the OSC 133 marks we already emit** | 1.3.0 | `internal/shellinit/` already injects the marks and the daemon reads only `D` (`detectOSC133Exit`). Coverage is uneven and part of the work: bash and zsh emit A/B/D, PowerShell emits A and D but **no B**, and fish, sh and cmd.exe get no injection at all (`shellinit.go:52`). PowerShell needs the `B` mark added before anything timing-based works on Quil's flagship platform. Ghostty turns the same marks into jump-to-prompt, select-a-command's-whole-output, click-to-move-cursor inside the prompt, and "don't confirm close while the cursor sits at a prompt". We already pay the cost and take almost none of the value. Cheapest large win on this page. | M |
| G2 | **Key tables** (modal binding sets) | 1.3.0 | Named binding sets that activate and deactivate, plus `chain` (one key runs several actions) and `catch_all` (match any unbound key). `internal/keymap/` already does sequences and presets; key tables are the next layer, and they are what a real vim-style copy mode needs. | M |
| G3 | **A `quil +action` CLI** | since 1.0 | Ghostty ships `+list-themes`, `+list-keybinds`, `+list-actions`, `+show-config`, `+validate-config`, `+crash-report`. Quil has MCP for AI and nothing for humans or scripts — gap #7 above. The `+action` shape is a clean precedent that leaves the existing subcommands alone. | M |
| G4 | **A duration floor on command-finish alerts** | 1.3.0 | `notify-on-command-finish-after` fires only when the command ran longer than N. Quil's `detectOSC133Exit` emits a `command_complete` for every command however short, with no duration attached. It is quiet today only because the `commands` event group is off by default (`EventGroupsConfig.Commands`) — turn it on, or consume the events over MCP, and a one-second command arrives exactly like a ten-minute one. A floor is what would make that group usable rather than noisy. Needs the `B` mark timestamped per pane, which is G1's work and does not exist on PowerShell yet. | S–M |
| G5 | **Rich-text and raw-VT clipboard copy** | 1.3.0 | `copy_to_clipboard` takes `mixed`, `plain`, `html` or `vt`. Colours survive a paste into a document, and `vt` keeps the escape sequences intact. Useful for pasting agent output into a ticket. | S–M |
| G6 | **Read-only pane mode** | 1.3.0 | A surface that refuses input to the PTY and warns on close. A good fit for a pane whose agent is mid-turn. | S |
| G7 | **`theme = light:X,dark:Y` auto-switch** | 1.2 | Quil renders against the terminal's own OSC 10/11 colours and ships no presets, which is a defensible choice. If that ever changes, Ghostty's light/dark pair is the shape to copy. | M |

Explicitly **not** worth chasing: GPU shaders, font shaping and ligatures, the
quick terminal, background images, Metal/OpenGL rendering. Those are emulator
jobs, and Quil is not an emulator.

---

## Where Quil already leads (defend these)

- **Native Windows maturity — but this moat narrowed in 2026-09.** Quil is still
  the most polished of the three on Windows: bundled ConPTY + OpenConsole for
  Win10, DIB→PNG clipboard image-paste proxy, window-size persistence, conhost
  grid fixups, ConPTY ghost-window guards. aoe is still **WSL2-only**. herdr,
  however, no longer says *beta* — it calls native Windows generally available.
  What is left of the wedge is a specific exclusion list rather than a maturity
  gap: on herdr there is no direct terminal attach, no live server handoff, and
  no clipboard image bridge in local native panes. Marketing copy that leans on
  "herdr is beta on Windows" is now false and must be replaced with those three
  exclusions.
  **Do not claim Windows-as-a-remote-host as a Quil advantage.** herdr excludes
  it, but so does Quil — `docs/roadmap/remote-daemon.md` states plainly that
  Windows remotes are unsupported (no `uname`, no `sh`, and a running Windows
  executable cannot be replaced in place). It is a shared limitation of the
  category, not a wedge, and it was written into the site copy once during the
  2026-09-10 pass before being caught.
- **First-class MCP server.** Quil exposes 35 MCP tools that Claude Desktop /
  Cursor / VS Code consume *natively*, including projects, remote hosts and
  pane-to-pane task delegation. The competitors built bespoke socket/HTTP
  APIs that need an "agent skill" to teach. For the *AI-agent-as-operator* use
  case, Quil's protocol choice is the strongest of the three.
- **Pane notes** — per-pane markdown editor with autosave. Neither competitor has
  anything like it.
- **Memory reporting** — per-pane Go-heap + PTY RSS with dedicated MCP tools.
  Unique.
- **No telemetry, no account** — shared with herdr (aoe has opt-in telemetry).

---

## Strategic reading

Closing the most impactful gaps, roughly in ROI order:

1. **Agent breadth + screen-heuristic detection** (#1, #2) — supporting only two
   agents is the starkest deficit; both rivals detect many out of the box. A
   screen-content detector (like herdr's TOML manifests) would let Quil claim
   broad agent awareness without per-agent hooks.
2. **A diff viewer** (#4) — the other half of the most-cited reason people adopt
   these tools ("parallel agents on branches, then review the diff"). The
   worktree half of that pair (#3) has since shipped, which makes the review
   layer the one that is now conspicuously missing: Quil can put an agent on its
   own branch and can no longer show you what it did there.
3. **A shell-scriptable CLI** (#7) — Quil's MCP is great for AI but leaves
   human/script orchestration unserved; a thin CLI over the existing daemon IPC is
   low effort.
4. **Sound, and desktop notifications beyond Windows** (#14, #15) — small effort,
   immediately felt. Windows toasts shipped; macOS and Linux did not, and neither
   did sound on any platform.
5. **Web/mobile access** (#9, #10) — the largest builds; likely a deliberate
   "not now" given Quil's TUI/Windows-native focus, but this is where aoe is
   pulling away for the mobile crowd. Terminal-side remote attach (#8) is no
   longer part of this cluster — it shipped in v1.44 — and neither is
   sandboxing (#11), which shipped as Docker sandbox panes.
</content>
</invoke>
