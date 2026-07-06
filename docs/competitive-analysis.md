# Competitive Analysis — herdr & Agent of Empires

> Deep comparison of Quil against the two closest direct competitors in the
> "terminal multiplexer for AI coding agents" category, captured 2026-07-06.
> Feeds the [competitive-gap section of the roadmap](roadmap.md#planned--competitive-gaps-herdr-aoe).

The classic-multiplexer comparisons (tmux, zellij, WezTerm, screen) live on the
marketing site under `/vs/*`. This document covers the two products that share
Quil's actual thesis — *persistent, agent-aware multiplexing* — and are therefore
the more honest mirror of where Quil leads and where it trails.

- **herdr** — <https://github.com/ogulcancelik/herdr> (Rust, AGPL-3.0 + commercial)
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
| **Windows** | ✅ **Native** (bundled ConPTY/OpenConsole) | ⚠️ Native **beta** (ConPTY) | ❌ **WSL2 only** |
| Agent-drives-it API | **MCP server** (18 tools, native protocol) | Socket API + full CLI + agent skill | HTTP REST API (130 routes) + CLI |
| Web/browser UI | ❌ TUI only | ❌ (responsive TUI) | ✅ **React PWA dashboard** |
| Container sandbox | ❌ | ❌ | ✅ Docker/Podman/Apple |
| Remote phone access | ❌ | via SSH TUI | ✅ Tunnel + PWA + Web Push |
| Git worktree-per-session | ❌ | ✅ | ✅ (+ multi-repo) |
| AI agents supported | **2** deep + tools | **~18** detected, 14 integrations | **~13** terminal, 7 ACP |
| Scale | ~30–40k Go | ~170k Rust | ~292k Rust + large web app |
| License / backing | product | AGPL-3.0 + commercial, solo + sponsors | MIT, Mozilla.ai community |

**The blunt summary:** herdr is Quil's closest philosophical twin (own
multiplexer, single binary, socket API, native-Windows ambition) but far ahead on
agent breadth, plugins, and remote. aoe leans on tmux and pours its energy into a
web/mobile dashboard, an ACP "structured view", and container sandboxing that
Quil has nothing comparable to. Both competitors are considerably larger and
support many more agents. Quil's real moats are **native Windows maturity**, being
a **first-class MCP server**, and two unique niceties (**pane notes**,
**memory reporting**).

---

## Capability matrix

Legend: ✅ full · 🟡 partial/different · ❌ absent

### Core multiplexer

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Panes / splits (mixed H/V) | ✅ | 🟡 (tmux) | ✅ |
| Tabs | ✅ | 🟡 (tmux windows) | ✅ |
| Workspaces (top-level project container) | ✅ | ✅ (profiles/groups) | ❌ (tabs only) |
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
| Remote attach over SSH (thin client) | ✅ | 🟡 (SSH+web) | ❌ |
| Clipboard-image bridged into remote session | ✅ | ✅ (web) | ❌ |
| Live server handoff (upgrade without killing panes) | ✅ | 🟡 (detached workers) | ❌ |
| Web dashboard (browser terminal) | ❌ | ✅ (PWA) | ❌ |
| Remote phone access (tunnel + QR/passphrase) | ❌ | ✅ | ❌ |
| Web Push / mobile notifications | ❌ | ✅ (VAPID) | ❌ |

### Agent awareness

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Status rollup (blocked/working/done/idle) | ✅ | ✅ | ✅ |
| Screen-content heuristic detection (no hooks) | ✅ | ✅ | 🟡 (idle patterns only) |
| Hook/integration-based state | ✅ | ✅ | ✅ (claude + opencode) |
| Runtime-updatable detection manifests | ✅ (remote fetch) | ❌ | ❌ |
| Detection debugger (`agent explain`) | ✅ | ❌ | ❌ |
| Model/context-token status display | 🟡 | ✅ | ✅ |
| AI agents with detection | ~18 | ~13 | **2** |
| One-command agent integration installer | ✅ | ✅ | ❌ |
| Native agent session restore | ✅ (14 agents) | ✅ (Claude+) | ✅ (claude, opencode) |
| Session fork | ❌ | ✅ | ❌ |
| Session import from disk | ❌ | ✅ (Claude) | ❌ |

### Git / repos

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Worktree-per-session (auto branch + worktree) | ✅ | ✅ | ❌ |
| Multi-repo workspace (one session, N repos) | ❌ | ✅ | ❌ |
| Built-in diff viewer (review + edit) | ❌ | ✅ | ❌ |
| Inline diff comments → prompt to agent | ❌ | ✅ | ❌ |
| Lazygit / git-tool integration | 🟡 (plugin) | ✅ (tool sessions) | ✅ (Alt+G overlay) |

### Sandboxing / isolation

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Docker container sandbox | ❌ | ✅ | ❌ |
| Podman / Apple Containers | ❌ | ✅ | ❌ |
| Shared auth volumes (in-container login) | ❌ | ✅ | ❌ |

### Extensibility

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Declarative pane-type plugins (TOML) | 🟡 | 🟡 | ✅ |
| Executable plugins (any language) | ✅ | ✅ (design) | ❌ |
| Plugin actions / event hooks / link handlers | ✅ | ✅ | ❌ |
| Plugin marketplace (GitHub topic index) | ✅ | ✅ (featured + hash) | ❌ |
| Agent-drives-multiplexer API | ✅ socket+CLI | ✅ HTTP+CLI | ✅ **MCP (18 tools)** |
| General CLI to script panes | ✅ | ✅ | ❌ |
| MCP server forwarding to agents | ❌ | ✅ | ❌ |
| ACP structured view (plan/tool/approve cards) | ❌ | ✅ | ❌ |
| Command palette | 🟡 | ✅ (web) | ❌ |

### Config / UX / ops

| Feature | herdr | aoe | Quil |
|---|:---:|:---:|:---:|
| Themes (multiple, light/dark auto) | ✅ (18) | ✅ (8) | 🟡 minimal |
| Sound notifications | ✅ | ✅ | ❌ |
| OS/terminal desktop notifications | ✅ | ✅ (push) | ❌ (in-TUI sidebar) |
| In-TUI notification center | 🟡 | ✅ | ✅ |
| Repo config + lifecycle hooks | 🟡 | ✅ | ❌ |
| Profiles (per-project workspaces) | 🟡 | ✅ | ❌ |
| Auto-stop idle sessions | 🟡 | ✅ | ❌ |
| Groups / favorites / snooze / archive / trash | 🟡 | ✅ | ❌ |
| Self-update command | ✅ | ✅ | ❌ (install.sh only) |
| Pane notes (per-pane editor) | ❌ | ❌ | ✅ |
| Memory reporting (heap + PTY RSS) | ❌ | ❌ | ✅ |
| Windows clipboard image-paste proxy | ⚠️ unverified | ❌ | ✅ |
| Input / command history per pane | 🟡 | ✅ | ✅ |

---

## The 20 features worth chasing

The most interesting capabilities Quil lacks or only partially supports, ranked
by a blend of strategic impact and how differentiating they are. Effort is a rough
T-shirt size. "Maps to" links a gap to an existing roadmap item it extends.

| # | Feature | Source | Why it matters | Effort | Impact | Maps to |
|---|---|---|---|:---:|:---:|---|
| 1 | Screen-content agent state detection (no hooks) | herdr, aoe | Blocked/working/done inferred from terminal output for *any* agent, zero hooks. Quil only pattern-matches idle. | M | ★★★ | process-health |
| 2 | Broad agent support + detection registry | herdr, aoe | They detect ~13–18 agents (Codex, Gemini, Cursor, Copilot, Droid, Devin…); Quil ships 2. Starkest gap. | M | ★★★ | community-plugins |
| 3 | Git worktree-per-session | herdr, aoe | Auto branch + worktree on session create, cleanup on delete. The #1 adoption driver for these tools. | M | ★★★ | workspace-files |
| 4 | Built-in diff viewer (review + edit + commit) | aoe | Review agent changes without leaving the TUI. Table stakes for "review what the agent did". | M | ★★★ | new |
| 5 | Executable/scriptable plugins (actions, event hooks, link handlers) | herdr, aoe | Any-language plugins that run logic, not just declare pane types. Unlocks a real ecosystem. | M | ★★★ | community-plugins, cross-pane-events |
| 6 | Plugin marketplace (GitHub-topic index) | herdr, aoe | Discover + `install owner/repo`. Already partially planned. | M | ★★ | community-plugins |
| 7 | General shell CLI to script the multiplexer | herdr, aoe | `quil pane split`, `quil tab create` from any script. MCP serves AI; humans/scripts have nothing. | M | ★★ | new |
| 8 | Remote SSH thin-client attach (`--remote`) | herdr | Local client of a remote server; bridges local clipboard image paste into remote agents. | M | ★★ | session-sharing |
| 9 | Web dashboard (browser terminal) | aoe | Real terminal + diffs in the browser, installable PWA. The largest surface Quil is missing. | L | ★★★ | new |
| 10 | Remote phone access (tunnel + QR/passphrase + push) | aoe | Check on agents from a phone via Tailscale/Cloudflare with two-factor pairing. | L | ★★ | session-sharing |
| 11 | Container sandboxing (Docker/Podman) + shared auth volumes | aoe | Isolate agents in containers; authenticate in-container without re-login. | L | ★★ | new |
| 12 | Multi-repo workspaces | aoe | One session/branch spanning several repos. | M | ★★ | workspace-files |
| 13 | Inline diff comments → prompt to agent | aoe | Annotate a diff; comments assemble into one prompt back to the agent. Tight review loop. | M | ★★ | (extends #4) |
| 14 | Sound notifications | herdr, aoe | Audible cue when an agent needs you. Cheap, immediately felt. | S | ★★ | notification-center |
| 15 | OS/desktop notifications (beyond in-TUI sidebar) | herdr, aoe | OSC/`notify-send`/`terminal-notifier` so alerts leave the TUI (works over SSH). | S | ★★ | notification-center |
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

## Where Quil already leads (defend these)

- **Native Windows maturity.** Quil is the most polished of the three on Windows:
  bundled ConPTY + OpenConsole for Win10, DIB→PNG clipboard image-paste proxy,
  window-size persistence, conhost grid fixups, ConPTY ghost-window guards. herdr
  is explicitly *beta* on Windows; aoe is **WSL2-only**. A real, defensible wedge
  with Windows-based agent developers.
- **First-class MCP server.** Quil exposes 18 MCP tools that Claude Desktop /
  Cursor / VS Code consume *natively*. The competitors built bespoke socket/HTTP
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
2. **Worktree-per-session + a diff viewer** (#3, #4) — the single most-cited
   reason people adopt these tools ("parallel agents on branches, then review the
   diff"). Quil has the git-discovery primitives already; the automation and
   review layers are missing.
3. **A shell-scriptable CLI** (#7) — Quil's MCP is great for AI but leaves
   human/script orchestration unserved; a thin CLI over the existing daemon IPC is
   low effort.
4. **Sound + OS notifications** (#14, #15) — small effort, immediately felt.
5. **Web/remote access & sandboxing** (#9, #10, #11) — the largest builds; likely
   a deliberate "not now" given Quil's TUI/Windows-native focus, but this is where
   aoe is pulling away for the mobile/remote crowd.
</content>
</invoke>
