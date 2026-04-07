// Feature catalog driving the home feature grid and the /features
// deep dive. Bullets come from the Quil README lines 27-95, PRD
// section 4, and ROADMAP milestones M1-M13.

export type IconName =
  | "refresh-ccw"
  | "sparkles"
  | "layers"
  | "puzzle"
  | "bell"
  | "monitor"
  | "notebook-pen"
  | "zap"
  | "terminal"
  | "save"
  | "code"
  | "layout-panel-left";

export interface Feature {
  slug: string;
  icon: IconName;
  title: string;
  blurb: string;
  detail: string[];
  category: "persistence" | "interaction" | "ai" | "extensibility" | "observability";
}

export const features: Feature[] = [
  // --- Persistence ---------------------------------------------------
  {
    slug: "reboot-proof-sessions",
    icon: "refresh-ccw",
    title: "Reboot-proof sessions",
    blurb:
      "Workspaces survive full host reboots. Type `quil` after a restart and everything snaps back.",
    category: "persistence",
    detail: [
      "Continuous snapshot of tabs, panes, layout, and working directories to ~/.quil/workspace.json.",
      "Ghost buffers render the last 500 lines of each pane instantly while shells re-initialise.",
      "Pane split tree is serialised to JSON and restored on reconnect — same horizontal/vertical nesting, same ratios.",
      "Target boot-to-productive time: under 30 seconds.",
    ],
  },
  {
    slug: "live-cwd-tracking",
    icon: "layout-panel-left",
    title: "Live CWD tracking",
    blurb:
      "Pane borders show the shell's current directory in real time — no config, no manual hooks.",
    category: "persistence",
    detail: [
      "Auto-injects OSC 7 hooks into bash, zsh, and PowerShell at spawn time.",
      "Fish emits OSC 7 natively, no injection needed.",
      "The directory shown on the pane border updates on every cd, pushd, and popd.",
    ],
  },

  // --- AI ------------------------------------------------------------
  {
    slug: "ai-session-resume",
    icon: "sparkles",
    title: "AI session resume",
    blurb:
      "Claude Code conversations resume automatically after a reboot. No copy-paste, no context rebuild.",
    category: "ai",
    detail: [
      "Each AI pane gets a UUID at creation time. On restart Quil runs `claude --resume <session-id>` automatically.",
      "Works for any AI tool that exposes a session ID — Claude Code today, more to come.",
      "For tools without a session ID, plugins can fall back to regex scraping the last state or replaying a command.",
    ],
  },
  {
    slug: "mcp-server",
    icon: "zap",
    title: "MCP server for AI agents",
    blurb:
      "Run `quil mcp` and an AI agent can list panes, read output, send keystrokes, and snapshot your workspace.",
    category: "ai",
    detail: [
      "13 tools exposed over the Model Context Protocol (Anthropic's open standard for AI tool use).",
      "Tools include: list panes, read pane output, send keys, switch tabs, screenshot a pane, and more.",
      "Lets any MCP-capable client (Claude Desktop, Claude Code, Cursor) reach directly into your running Quil session.",
    ],
  },

  // --- Interaction ---------------------------------------------------
  {
    slug: "typed-panes",
    icon: "layers",
    title: "Typed panes",
    blurb:
      "Terminals are not all the same. Quil understands pane types and gives each one context-aware behaviour.",
    category: "interaction",
    detail: [
      "Four built-in pane types: Terminal, Claude Code, SSH, Stripe.",
      "Each type has its own resume strategy, error handler, and status line.",
      "User-definable additional types via TOML plugin files in ~/.quil/plugins/.",
    ],
  },
  {
    slug: "tmux-splits",
    icon: "layout-panel-left",
    title: "tmux-style splits",
    blurb:
      "Arbitrarily nested horizontal and vertical splits with mouse click hit-testing.",
    category: "interaction",
    detail: [
      "Binary split tree, each split with its own direction and ratio.",
      "Click any pane to focus it; scroll wheel traverses terminal history.",
      "Focus mode (Ctrl+E) expands the active pane full-screen while others keep running in the background.",
    ],
  },
  {
    slug: "pane-notes",
    icon: "notebook-pen",
    title: "Pane notes",
    blurb:
      "Alt+E opens a plain-text editor beside any pane. Notes save automatically and travel with the workspace.",
    category: "interaction",
    detail: [
      "Markdown-compatible plain text, rendered as the pane loses focus.",
      "30-second debounce auto-save, Ctrl+S for explicit save.",
      "Side-by-side layout so you can take notes while the pane keeps producing output.",
    ],
  },

  // --- Extensibility -------------------------------------------------
  {
    slug: "plugin-system",
    icon: "puzzle",
    title: "TOML plugin system",
    blurb:
      "Declare a new pane type in a single TOML file. No compilation, no restart, hot-reload on save.",
    category: "extensibility",
    detail: [
      "Plugin definitions live in ~/.quil/plugins/<name>.toml.",
      "Sections: [plugin], [spawn], [keys], [resume], [error], [status] — each optional.",
      "Declarative config means no shell scripting footguns; the daemon validates the TOML at load time.",
    ],
  },

  // --- Observability -------------------------------------------------
  {
    slug: "notification-center",
    icon: "bell",
    title: "Notification center",
    blurb:
      "Quil detects when a pane exits, errors, or goes idle and surfaces it in a dedicated sidebar.",
    category: "observability",
    detail: [
      "Daemon-side event queue with pattern-matching idle analysis.",
      "Process exit detection with exit-code extraction.",
      "Optional sidebar surfaces notifications without interrupting focused work.",
    ],
  },

  // --- Cross-platform ------------------------------------------------
  {
    slug: "cross-platform",
    icon: "monitor",
    title: "Cross-platform from day one",
    blurb:
      "Native Linux, macOS, and Windows support. No WSL required.",
    category: "interaction",
    detail: [
      "PTY via creack/pty on Unix, ConPTY on Windows.",
      "IPC via Unix domain sockets on Linux/macOS, Named Pipes on Windows.",
      "Pre-built binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64.",
    ],
  },
];

/** Home page feature grid — 6 most important features, shown as cards. */
export const homeFeatures: Feature["slug"][] = [
  "reboot-proof-sessions",
  "ai-session-resume",
  "typed-panes",
  "plugin-system",
  "notification-center",
  "cross-platform",
];

export const featureCategories: Record<Feature["category"], string> = {
  persistence: "Persistence",
  interaction: "Interaction",
  ai: "AI integration",
  extensibility: "Extensibility",
  observability: "Observability",
};
