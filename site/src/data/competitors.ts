// Shared comparison matrix for /vs/* pages.
//
// Single source of truth: edit this file once and every comparison
// page updates. Each row is a feature with per-product support level
// (yes / no / partial) and an optional note explaining nuance.

export type Support = "yes" | "no" | "partial";

export interface CompareRow {
  feature: string;
  quil: Support;
  tmux: Support;
  zellij: Support;
  wezterm: Support;
  screen: Support;
  /** Optional footnote rendered below the row in the matrix. */
  note?: string;
}

export const competitorMatrix: CompareRow[] = [
  {
    feature: "Session persistence while the multiplexer server is running",
    quil: "yes",
    tmux: "yes",
    zellij: "yes",
    wezterm: "yes",
    screen: "yes",
  },
  {
    feature: "Survives a full host reboot",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
    note: "Quil's defining capability. Everyone else loses the session on reboot.",
  },
  {
    feature: "AI session auto-resume (Claude Code, OpenCode)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Remote attach: local client, sessions on another host",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "yes",
    screen: "no",
    note: "tmux, Zellij and Screen run their client ON the remote — you ssh in first, and a dropped link means re-attaching by hand. WezTerm's mux domains and Quil's `quil --remote <host>` both keep the client local and reconnect by themselves. Quil opens no port on the server (it is one `ssh -T` channel) and installs itself on a bare host.",
  },
  {
    feature: "Agent state per group, visible at a glance",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
    note: "Not \"does it have a sidebar\" — the claim is that the multiplexer knows an agent is mid-turn, versus waiting on a permission prompt, versus finished. That needs a feed from the agent itself; Quil reads Claude Code and OpenCode hook events. Every project row carries a roll-up of its agents and keeps updating while you are looking at a different project, which is the point: the one that got stuck is the one you are not watching.",
  },
  {
    feature: "Several hosts' sessions side by side in one client",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "yes",
    screen: "no",
    note: "WezTerm's mux domains genuinely do this — you can attach several and mix their tabs in one window. tmux, Zellij and Screen run the client on the remote, so a second host means a second terminal. Quil dials each host over its own `ssh -T` channel, gives each its own reconnect state, and groups their projects in the same sidebar; one daemon dying leaves the rest interactive.",
  },
  {
    feature: "Typed panes (Terminal / AI / SSH / Webhook)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Plugin system",
    quil: "yes",
    tmux: "partial",
    zellij: "yes",
    wezterm: "yes",
    screen: "no",
    note: "Quil uses declarative TOML. Zellij uses WASM. WezTerm uses Lua. tmux uses shell scripts.",
  },
  {
    feature: "Mouse support",
    quil: "yes",
    tmux: "yes",
    zellij: "yes",
    wezterm: "yes",
    screen: "no",
  },
  {
    feature: "Ghost buffers (last 500 lines instant on reconnect)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "partial",
    screen: "no",
  },
  {
    feature: "Command palette (fuzzy action + pane launcher)",
    quil: "yes",
    tmux: "partial",
    zellij: "partial",
    wezterm: "yes",
    screen: "no",
    note: "WezTerm's is built in (Ctrl+Shift+P, frecency-ranked). tmux has a command prompt but it is not fuzzy. Zellij's comes from community plugins (cmd-pal), not the core.",
  },
  {
    feature: "Search every pane's scrollback from one prompt",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
    note: "Every other tool searches one pane at a time in copy mode — including WezTerm, whose palette is command-only. Across panes, tmux needs the third-party tmux-grep plugin. In Quil it is the same box as the command palette: type, and matching panes appear under `Found in panes` with a preview and a match count.",
  },
  {
    feature: "MCP server for AI agents",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Notification center + idle analysis",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Pane notes editor (Alt+E)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Windows native (no WSL)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "yes",
    screen: "no",
  },
  {
    feature: "Declarative keybindings (config file)",
    quil: "yes",
    tmux: "yes",
    zellij: "yes",
    wezterm: "yes",
    screen: "yes",
  },
];

/**
 * A row in a competitor-specific matrix (used for the AI-agent-orchestrator
 * comparisons herdr / aoe, whose feature axis differs from the classic
 * multiplexers). `them` is the competitor's support level.
 */
export interface VsRow {
  feature: string;
  quil: Support;
  them: Support;
  note?: string;
}

export interface CompetitorInfo {
  slug: "tmux" | "zellij" | "wezterm" | "screen" | "herdr" | "aoe";
  name: string;
  description: string;
  positioning: string;
  keyStrength: string;
  keyGap: string;
  migrationNote: string;
  /**
   * Optional competitor-specific matrix. When present it replaces the shared
   * `competitorMatrix` on that page — used for herdr/aoe, which are direct
   * agent-orchestrator rivals rather than classic multiplexers.
   */
  matrix?: VsRow[];
  /** Optional override for the "where Quil takes over" headline. */
  quilHeadline?: string;
  /**
   * Optional <title> override, used verbatim (ComparePage suppresses the
   * brand suffix, so "Quil vs tmux · Quil" can't happen).
   *
   * The default stem is "Quil vs <name>", which targets a phrase almost
   * nobody types — the query with the demand is "<name> alternative".
   * Set this where a competitor has a real alternative-seeking audience;
   * leave it unset for herdr/aoe, whose names are searched directly.
   */
  seoTitle?: string;
  /** Per-page FAQ — 3-4 Q&A pairs. */
  faq: { question: string; answer: string }[];
}

export const competitors: Record<CompetitorInfo["slug"], CompetitorInfo> = {
  tmux: {
    slug: "tmux",
    name: "tmux",
    seoTitle: "Quil vs tmux — a tmux alternative that survives reboots",
    description:
      "The de-facto Unix terminal multiplexer. Server-side sessions, scriptable plugins, steep learning curve, shipped by default on most distros alongside screen.",
    positioning:
      "tmux is great for what it does — a stable, battle-tested server multiplexer built in 2007 — but it was never designed to survive a host reboot, understand AI coding sessions, or treat different panes as different types of work. Quil is for the problem tmux doesn't solve.",
    keyStrength:
      "Ubiquity, stability, and the largest plugin ecosystem of any multiplexer. If you need a standard tool on a standard Unix host, tmux is still the answer.",
    keyGap:
      "Zero persistence across host reboots. You can bolt on tmux-resurrect or tmux-continuum, but even those only restore layout and working directories — not AI session state, not running processes, not scrollback.",
    migrationNote:
      "Coming from tmux? Quil uses familiar keybindings (Ctrl+T new tab, Alt+H / Alt+V to split). Everything's remappable in ~/.quil/config.toml so you can reuse your tmux muscle memory verbatim.",
    faq: [
      {
        question: "Can I run Quil and tmux side by side?",
        answer:
          "Yes. They don't share sockets or state, so you can experiment with Quil without touching your tmux setup. Some users run Quil for AI-heavy projects and tmux for traditional admin sessions.",
      },
      {
        question: "Does Quil read my tmux sessions?",
        answer:
          "Not currently — Quil maintains its own workspace state under ~/.quil/. A tmux import helper is on the future roadmap.",
      },
      {
        question: "Is Quil a tmux replacement?",
        answer:
          "It depends on what you use tmux for. If you need reboot persistence + AI session continuity, yes. If you need a headless multiplexer for classical server administration, tmux remains the right tool.",
      },
      {
        question: "Is Quil a good tmux alternative for AI coding agents?",
        answer:
          "For AI-heavy work, yes. tmux keeps sessions alive while its server runs, but it loses everything on a host reboot and has no concept of an AI session. Quil survives a full reboot, auto-resumes Claude Code and OpenCode sessions with the current (post-rotation) session id, and exposes an MCP server so an AI assistant can read and drive the panes directly. For classic headless server administration, tmux is still the better fit.",
      },
      {
        question: "What is the best tmux alternative that survives a reboot?",
        answer:
          "tmux, Zellij, WezTerm and GNU Screen all lose their sessions when the host reboots — their persistence only lasts while the server process is alive. Quil was built specifically for this gap: its daemon snapshots the whole workspace to disk continuously, so after a reboot you type one command and the layout, working directories, scrollback, and AI sessions come back in under 30 seconds.",
      },
    ],
  },

  zellij: {
    slug: "zellij",
    name: "Zellij",
    seoTitle: "Quil vs Zellij — what a reboot does to each",
    description:
      "Modern Rust terminal multiplexer with a friendly UX, WASM plugins, and sane defaults. Released in 2021.",
    positioning:
      "Zellij is the closest competitor on UX — both tools prioritise gentle defaults and a modern feel. Where they diverge: Zellij is a multiplexer first, Quil is a workflow orchestrator first. Quil's typed panes, AI session resume, and reboot persistence address a problem Zellij considers out of scope.",
    keyStrength:
      "Modern UX, clean WASM plugin model, excellent defaults, discoverable status bar. Zellij users rarely need to read a manual.",
    keyGap:
      "No concept of reboot-proof workflows or AI session continuity. Zellij sessions are runtime-only; a host reboot is final.",
    migrationNote:
      "Zellij users will feel at home in Quil — both tools use Alt-based keys and avoid prefix chords by default. The main adjustment is Quil's typed panes (Terminal / AI / SSH / etc.), which Zellij doesn't have.",
    faq: [
      {
        question: "Is Quil just Zellij with AI support?",
        answer:
          "It's more than that. Quil's core bet is that a workflow orchestrator is a different tool from a multiplexer. Typed panes, ghost buffers, the MCP server, and the pane notes editor all follow from that bet. Zellij has chosen a different scope.",
      },
      {
        question: "Can Quil use Zellij plugins?",
        answer:
          "No. Zellij plugins are WASM modules using Zellij's host API. Quil plugins are TOML declarations. The two models are intentionally different — Quil's is simpler but less dynamic, Zellij's is more powerful but harder to write.",
      },
      {
        question: "Both tools are in Rust, right?",
        answer:
          "Zellij is Rust. Quil is Go — specifically Go 1.25, with Bubble Tea v2 for the TUI. Different ecosystems, same aesthetic goals.",
      },
    ],
  },

  wezterm: {
    slug: "wezterm",
    name: "WezTerm",
    seoTitle: "Quil vs WezTerm — what survives when the terminal closes",
    description:
      "A GPU-accelerated cross-platform terminal emulator with built-in multiplexer features, written in Rust, extensible via Lua. Released by Wez Furlong.",
    positioning:
      "WezTerm is a terminal emulator that happens to have multiplexer features. Quil is a workflow orchestrator that happens to render terminals. The category is different: WezTerm cares about glyph rendering, ligatures, and GPU compositing. Quil cares about what happens when you reboot.",
    keyStrength:
      "Best-in-class rendering, GPU-accelerated, ligature support, Lua extensibility, built-in SSH multiplexer. WezTerm is one of the most polished terminal emulators available.",
    keyGap:
      "No reboot persistence, no AI session awareness, no typed panes. WezTerm's multiplexer is an escape hatch for when you need tabs without a separate multiplexer — it's not the core of the product.",
    migrationNote:
      "Keep WezTerm as your terminal emulator and run Quil inside it. The two are complementary: WezTerm handles rendering (fonts, ligatures, GPU acceleration), Quil handles workflow (persistence, AI sessions, typed panes).",
    faq: [
      {
        question: "Can I use WezTerm and Quil together?",
        answer:
          "Yes, and we recommend it. WezTerm renders Quil beautifully, and Quil's persistence layer complements WezTerm's lack of reboot survival.",
      },
      {
        question: "Does Quil have its own terminal emulator?",
        answer:
          "No. Quil is a TUI that runs inside whatever terminal emulator you already use — WezTerm, Alacritty, Kitty, Windows Terminal, Ghostty, iTerm2, the Linux console — anything with PTY support works.",
      },
      {
        question: "Why not just use WezTerm's multiplexer?",
        answer:
          "WezTerm's multiplexer is fine for tabs and splits but doesn't survive a reboot or understand pane types. If those features don't matter to you, WezTerm alone is plenty.",
      },
    ],
  },

  screen: {
    slug: "screen",
    name: "GNU Screen",
    seoTitle: "Quil vs GNU Screen — persistence in 2026",
    description:
      "The original Unix terminal multiplexer, first released in 1987. Still shipped by default on most Unix distributions.",
    positioning:
      "Screen is what every serious Unix admin learned first. It's stable, tiny, and still works on systems where tmux isn't installed. But it's a product of its era: no mouse support, no modern plugin model, no AI awareness, no reboot persistence, and a config syntax from another century.",
    keyStrength:
      "Ships by default on virtually every Unix host. Minimal dependencies. Works on systems where you can't install anything else. A legitimate fallback when you SSH into a hardened server.",
    keyGap:
      "1987-era UX. No mouse support by default. Config syntax that nobody enjoys writing. Zero AI integration. No reboot persistence. No typed panes.",
    migrationNote:
      "If you're still on screen for everyday work, any modern multiplexer is a straight upgrade. Quil's sweet spot is if you want the modern UX and also the reboot-proof persistence that even tmux doesn't give you.",
    faq: [
      {
        question: "Is Quil smaller than screen?",
        answer:
          "No. Quil ships as two binaries (quil + quild) totalling around 40 MB. Screen is a single ~1 MB binary. If binary size is your constraint, screen wins.",
      },
      {
        question: "Can Quil replace screen on a headless server?",
        answer:
          "Technically yes, but Quil is designed for interactive developer workflows. For pure detach-and-reattach on a headless host, screen and tmux are better targeted.",
      },
      {
        question: "Is screen actively maintained?",
        answer:
          "Yes, but slowly. Version 5.0 shipped in 2024 — the first major release since 2014.",
      },
      {
        question: "What is a modern alternative to GNU Screen?",
        answer:
          "Any current multiplexer — tmux, Zellij, or Quil — is a straight upgrade from Screen's 1987-era UX, mouse-less defaults, and dated config syntax. Quil goes further than the others by surviving a full host reboot and auto-resuming AI coding sessions, which even tmux doesn't do. If you only need detach-and-reattach on a headless server, tmux is the closest like-for-like replacement.",
      },
      {
        question: "Does Quil run on Windows, unlike GNU Screen?",
        answer:
          "Yes. GNU Screen is Unix-only. Quil ships a native Windows binary using ConPTY — no WSL required — alongside its Linux and macOS builds, so it's a cross-platform alternative to Screen for developers who also work on Windows.",
      },
    ],
  },

  herdr: {
    slug: "herdr",
    name: "herdr",
    description:
      "A Rust terminal multiplexer purpose-built for AI coding agents — 'tmux, rebuilt for agents.' Single binary, its own PTY and VT engine, a socket API agents can drive, and a language-agnostic plugin system. Quil's closest philosophical twin.",
    positioning:
      "herdr and Quil made almost the same bet: build your own multiplexer, keep it a single lightweight binary, and make it agent-aware. herdr is further ahead on agent breadth (it detects ~18 agents and ships 14 integrations) and scriptable plugins; Quil is further ahead on native Windows and on speaking MCP, the protocol AI assistants already understand. The honest read: herdr is the stronger Unix-first agent fleet manager today, Quil is the stronger Windows-native, MCP-native one.",
    keyStrength:
      "Agent breadth and extensibility. Detects ~18 agents with screen-content heuristics, installs native integrations with one command, exposes a full socket API + CLI that agents and scripts can drive, and runs language-agnostic plugins with actions, event hooks, and a GitHub-topic marketplace.",
    keyGap:
      "Native Windows is still beta, there is no MCP server (agents drive it through a bespoke socket API + an installed skill instead of a protocol assistants already speak), and it has no pane-notes editor or per-pane memory reporting.",
    migrationNote:
      "herdr uses a tmux-style prefix (Ctrl+B) where Quil uses direct Alt-based keys. Both keep agents alive on detach and restore AI sessions. If you're on Windows without WSL, Quil is the smoother path; if you drive many different agents from Unix and want to script the multiplexer from a shell, herdr is very strong.",
    quilHeadline: "Native Windows. MCP-native. Notes + memory built in.",
    matrix: [
      { feature: "Own multiplexer + PTY (not a tmux wrapper)", quil: "yes", them: "yes" },
      { feature: "Survives a full host reboot", quil: "yes", them: "yes" },
      { feature: "AI session auto-resume", quil: "yes", them: "yes", note: "herdr restores native sessions for ~14 agents; Quil for Claude Code + OpenCode." },
      { feature: "Native Windows (no WSL)", quil: "yes", them: "partial", note: "herdr's native Windows support is a ConPTY beta; Quil ships bundled ConPTY/OpenConsole and a Windows clipboard image-paste proxy." },
      { feature: "MCP server for AI agents", quil: "yes", them: "no", note: "herdr exposes a bespoke socket API + an installed agent skill instead of the MCP protocol." },
      { feature: "Pane notes editor", quil: "yes", them: "no" },
      { feature: "Per-pane memory reporting", quil: "yes", them: "no" },
      { feature: "Screen-content agent detection (no hooks)", quil: "partial", them: "yes", note: "Quil pattern-matches idle only; herdr ships updatable detection manifests for ~18 agents." },
      { feature: "Breadth of agents detected", quil: "partial", them: "yes", note: "Quil: 2 deep + tools. herdr: ~18." },
      { feature: "One-command agent integration installer", quil: "no", them: "yes" },
      { feature: "Git worktree-per-session", quil: "no", them: "yes" },
      { feature: "Executable plugins (actions / event hooks / link handlers)", quil: "partial", them: "yes", note: "Quil plugins are declarative TOML pane types; herdr runs any-language plugins." },
      { feature: "Plugin marketplace", quil: "no", them: "yes" },
      { feature: "General CLI to script the multiplexer", quil: "no", them: "yes", note: "Quil scripts via MCP (for AI); herdr adds a shell CLI for humans." },
      { feature: "Remote SSH thin-client attach", quil: "yes", them: "yes", note: "`quil --remote <host>` since v1.44; opens no port, reconnects on its own after a dropped link, and installs itself on a bare server." },
      { feature: "Several hosts in one window at once", quil: "yes", them: "no", note: "Since v1.47 a Quil client holds the local daemon and any number of remote ones together, each project tagged with the machine that owns it and each host with its own reconnect state. herdr's remote attach drives one server per client." },
      { feature: "Named sessions / live server handoff", quil: "no", them: "yes" },
      { feature: "Sound + desktop notifications", quil: "partial", them: "yes", note: "Quil has an in-TUI notification center but no sound or OS notifications." },
      { feature: "Themes with light/dark auto-switch", quil: "partial", them: "yes" },
    ],
    faq: [
      {
        question: "Is herdr basically Quil in Rust?",
        answer:
          "Architecturally they're remarkably close — both build their own multiplexer and PTY layer rather than wrapping tmux, both keep agents alive on detach, both restore AI sessions. The divergence is emphasis: herdr optimizes for agent breadth and shell scriptability on Unix; Quil optimizes for native Windows and for being an MCP server that AI assistants drive directly.",
      },
      {
        question: "Can herdr run on Windows without WSL?",
        answer:
          "It has an experimental native Windows (ConPTY) beta, but several features are unsupported there. Quil treats Windows as a first-class target with bundled ConPTY/OpenConsole and a clipboard image-paste proxy.",
      },
      {
        question: "How do agents control each tool?",
        answer:
          "herdr exposes a Unix-socket API plus a CLI, and ships an installable 'skill' so an agent learns to call it. Quil exposes 18 tools over the Model Context Protocol, which Claude Desktop, Cursor, and VS Code speak natively with no glue.",
      },
    ],
  },

  aoe: {
    slug: "aoe",
    name: "Agent of Empires",
    description:
      "A Rust TUI + React web dashboard that manages AI coding agents on top of tmux, with git worktrees, Docker sandboxing, a mobile-first 'structured view', and remote phone access. Backed by the Mozilla.ai community.",
    positioning:
      "Agent of Empires and Quil solve the same problem from opposite ends. AoE wraps tmux and invests everything above it — a browser dashboard, an Agent-Client-Protocol structured view, container sandboxing, and phone access over a tunnel. Quil builds its own multiplexer and invests in the terminal itself — native Windows, an MCP server, pane notes, and a remote mode that puts the panes on a server while the TUI stays on your laptop. On a phone or in a browser AoE is still far ahead; from a terminal, Quil now reaches a remote host without any of that stack.",
    keyStrength:
      "Reach and isolation. A real browser dashboard (installable PWA) with a native structured view of agent state, remote phone access via Tailscale/Cloudflare with QR + passphrase pairing and Web Push, git worktree-per-session and multi-repo workspaces, an in-TUI diff viewer, and Docker/Podman/Apple-Container sandboxing with shared auth volumes.",
    keyGap:
      "It depends on tmux, so native Windows is out (WSL2 only). It has no MCP server (external control is a bespoke HTTP API), no pane-notes editor, and no per-pane memory reporting — and its large surface area is a heavier install than Quil's two binaries.",
    migrationNote:
      "AoE keeps every agent in a tmux session, so you can `tmux attach` to any of them directly, and its dashboard runs in the browser. Quil is a native TUI with its own daemon — no tmux, Docker, or Node required — and runs natively on Windows. If you want the browser/mobile surface today, AoE leads; if you want a lightweight native terminal and an MCP server, Quil is the closer fit.",
    quilHeadline: "Native Windows. MCP-native. No tmux dependency.",
    matrix: [
      { feature: "Runs natively on Windows (no WSL / no tmux)", quil: "yes", them: "no", note: "AoE requires tmux, so Windows is WSL2-only. Quil is native on Windows." },
      { feature: "Survives a full host reboot", quil: "yes", them: "yes", note: "AoE via tmux; Quil via its own snapshot + ghost buffers." },
      { feature: "AI session auto-resume", quil: "yes", them: "yes" },
      { feature: "MCP server for AI agents", quil: "yes", them: "no", note: "AoE exposes a bespoke HTTP REST API; Quil speaks MCP natively." },
      { feature: "Pane notes editor", quil: "yes", them: "no" },
      { feature: "Per-pane memory reporting", quil: "yes", them: "no" },
      { feature: "Remote attach from a native terminal client", quil: "yes", them: "no", note: "`quil --remote <host>` runs the TUI locally against a daemon on the server, opens no port, reconnects on its own, and installs itself on a bare machine. AoE's remote surface is the browser dashboard — for a terminal you ssh in and attach tmux by hand." },
      { feature: "Web dashboard (browser terminal + diffs, PWA)", quil: "no", them: "yes" },
      { feature: "Remote phone access (tunnel + QR/passphrase + Web Push)", quil: "no", them: "yes" },
      { feature: "ACP structured view (plan / tool / approve cards)", quil: "no", them: "yes" },
      { feature: "Git worktree-per-session", quil: "no", them: "yes" },
      { feature: "Multi-repo workspaces", quil: "yes", them: "yes", note: "Was a genuine gap until v1.47. Quil projects each own a root directory and their own tabs, so several repositories sit side by side in one window — and a project can belong to a different machine, which AoE's workspaces do not span." },
      { feature: "Built-in diff viewer (review + edit)", quil: "no", them: "yes" },
      { feature: "Container sandboxing (Docker/Podman/Apple)", quil: "no", them: "yes" },
      { feature: "Screen-content agent detection (no hooks)", quil: "partial", them: "yes" },
      { feature: "Breadth of agents supported", quil: "partial", them: "yes", note: "Quil: 2 deep + tools. AoE: ~13 terminal + 7 ACP." },
      { feature: "Session fork / import from disk", quil: "no", them: "yes" },
      { feature: "Sound + push notifications", quil: "partial", them: "yes", note: "Quil has an in-TUI notification center but no sound or push." },
      { feature: "Session lifecycle mgmt (auto-stop idle, groups, archive)", quil: "no", them: "yes" },
    ],
    faq: [
      {
        question: "Does Agent of Empires work on Windows?",
        answer:
          "Only through WSL2 — it depends on tmux and POSIX process handling. Quil runs natively on Windows with bundled ConPTY/OpenConsole, so if you're a Windows developer it's the more direct fit.",
      },
      {
        question: "AoE has a web dashboard — does Quil?",
        answer:
          "Not today. AoE's browser dashboard and remote phone access are genuinely ahead here, and both are on Quil's roadmap. Quil's current bet is the native terminal experience plus an MCP server that lets an AI assistant drive the multiplexer directly.",
      },
      {
        question: "Which is lighter to run?",
        answer:
          "Quil ships two Go binaries and needs no tmux, Docker, or Node. AoE is a larger stack (tmux + a React app + optional Node ACP workers and containers) in exchange for its web and sandboxing features.",
      },
      {
        question: "Can I sandbox agents in containers with Quil?",
        answer:
          "Not yet — container sandboxing is an AoE strength that's on Quil's roadmap. For now Quil runs agents as normal processes with the same isolation as your shell.",
      },
    ],
  },
};
