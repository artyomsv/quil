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
  | "book-open"
  | "key-round"
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
      "15 tools exposed over the Model Context Protocol (Anthropic's open standard for AI tool use).",
      "Tools include: list/create/destroy panes, read pane output, send keys, switch tabs, screenshot a pane, watch the notification queue, and more.",
      "Lets any MCP-capable client (Claude Desktop, Claude Code, Cursor) reach directly into your running Quil session.",
    ],
  },
  {
    slug: "clipboard-image-paste",
    icon: "save",
    title: "Clipboard image paste",
    blurb:
      "Press paste on a screenshot and Quil decodes it, saves a PNG, and types the file path into the active pane — works around Claude Code's broken Windows clipboard reader.",
    category: "ai",
    detail: [
      "Win32 DIB / DIBV5 reader handles screenshots from Snipping Tool, Win+Shift+S, and other capture apps.",
      "Decodes 24bpp BI_RGB and 32bpp BI_BITFIELDS, including the all-zero-alpha promotion that catches apps which leave the alpha channel uninitialised.",
      "Files land in ~/.quil/paste/quil-paste-<ts>-<rand>.png with owner-only 0o600 / 0o700 permissions and an 8-byte crypto/rand suffix so a co-tenant can't enumerate them.",
      "Sidesteps the upstream Claude Code Windows clipboard image bug (anthropics/claude-code#32791) — any AI tool with file-reading tools picks the file up via the typed path.",
      "Three paste keys: Ctrl+V (default), Ctrl+Alt+V, and F8. F8 is the recommended Windows trigger because Windows Terminal eats Ctrl+V before it reaches the TUI.",
    ],
  },

  // --- Interaction ---------------------------------------------------
  {
    slug: "typed-panes",
    icon: "layers",
    title: "Typed panes",
    blurb:
      "Terminals are not all the same. Quil understands pane types and gives each one context-aware behaviour — including a per-spawn setup dialog with directory browser and runtime checkboxes.",
    category: "interaction",
    detail: [
      "Four built-in pane types: Terminal, Claude Code, SSH, Stripe.",
      "Each type has its own resume strategy, error handler, and status line.",
      "Pane setup dialog (opt-in via plugin TOML): a directory browser pre-filled with the active pane's CWD plus one checkbox per declared `[[command.toggles]]` entry. claude-code uses both — picks up the project's `.claude/` context automatically and offers a `Dangerously skip permissions` toggle for unattended runs.",
      "Toggle state rides through the existing `InstanceArgs` IPC field and survives daemon restarts; no IPC schema changes.",
      "User-definable additional types via TOML plugin files in ~/.quil/plugins/.",
    ],
  },
  {
    slug: "tmux-splits",
    icon: "layout-panel-left",
    title: "tmux-style splits with spatial navigation",
    blurb:
      "Arbitrarily nested horizontal and vertical splits with mouse hit-testing AND directional Alt+Arrow pane navigation that picks the closest neighbour, not the next leaf in the tree.",
    category: "interaction",
    detail: [
      "Binary split tree, each split with its own direction and ratio.",
      "Click any pane to focus it; scroll wheel traverses terminal history.",
      "Spatial pane navigation: Alt+Left/Right/Up/Down focuses the closest neighbour in that direction. Three tie-breakers (gap, perpendicular overlap, perpendicular center distance) match tmux/vim/iTerm muscle memory.",
      "Tab and Shift+Tab are deliberately NOT bound globally — they fall through to the PTY so shell completion and Claude Code's mode-cycling work naturally. Splits live on Alt+Shift+H / Alt+Shift+V to keep Alt+V free for Claude Code's image paste.",
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
  {
    slug: "leveled-logging",
    icon: "book-open",
    title: "Leveled logging + in-app log viewer",
    blurb:
      "A single `[logging] level` setting controls 152 existing log call sites and the new debug helpers. F1 opens read-only viewers for the client, daemon, and MCP logs.",
    category: "observability",
    detail: [
      "`internal/logger` wraps Go's stdlib `slog` and bridges every existing `log.Printf` call site at info level — old and new code respect one filter.",
      "Flip `[logging] level = \"debug\"` in config.toml to trace clipboard pipeline, per-key handler decisions, and Win32 image read step-by-step.",
      "F1 → About → View client log / daemon log / MCP logs opens a read-only TextEditor viewing the tail (256 KB) of each file. Symlink-rejecting via os.Lstat plus a re-stat through the open handle defeats TOCTOU swap.",
      "Alt+Up / Alt+Down jump the cursor by `[ui] log_viewer_page_lines` (default 40, configurable). The same `TextEditor.ReadOnly` flag is now available for any other look-but-don't-touch dialog.",
      "Hot-path Debug calls pre-check `slog.Enabled` so the fmt.Sprintf is skipped entirely when filtered out — important for the per-keystroke trace.",
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

/** Home page feature grid — 6 most important features, shown as cards.
 *  Currently unused: index.astro picks four hand-written topFeatures
 *  rather than reading from this list. Kept for the legacy /docs page
 *  and any future grid view that wants the canonical home selection. */
export const homeFeatures: Feature["slug"][] = [
  "reboot-proof-sessions",
  "ai-session-resume",
  "typed-panes",
  "clipboard-image-paste",
  "plugin-system",
  "cross-platform",
];

export const featureCategories: Record<Feature["category"], string> = {
  persistence: "Persistence",
  interaction: "Interaction",
  ai: "AI integration",
  extensibility: "Extensibility",
  observability: "Observability",
};
