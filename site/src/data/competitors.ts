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
    feature: "AI session auto-resume (Claude Code, Cursor)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
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

export interface CompetitorInfo {
  slug: "tmux" | "zellij" | "wezterm" | "screen";
  name: string;
  description: string;
  positioning: string;
  keyStrength: string;
  keyGap: string;
  migrationNote: string;
  /** Per-page FAQ — 3-4 Q&A pairs. */
  faq: { question: string; answer: string }[];
}

export const competitors: Record<CompetitorInfo["slug"], CompetitorInfo> = {
  tmux: {
    slug: "tmux",
    name: "tmux",
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
    ],
  },

  zellij: {
    slug: "zellij",
    name: "Zellij",
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
    ],
  },
};
