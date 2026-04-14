// Home page FAQ. Each Q&A pair drives both a visible <details>/<summary>
// accordion on the page AND a FAQPage JSON-LD entry for rich results.

export interface FaqItem {
  question: string;
  answer: string;
}

export const homeFaq: FaqItem[] = [
  {
    question: "What is Quil?",
    answer:
      "Quil is a reboot-proof terminal multiplexer for developers who run complex multi-tool workflows alongside AI coding assistants. It persists your entire workspace — tabs, panes, layout, working directories, and AI session IDs — across host reboots, so typing `quil` after a restart snaps everything back in under 30 seconds.",
  },
  {
    question: "How is Quil different from tmux or Zellij?",
    answer:
      "tmux and Zellij are terminal multiplexers — they survive network disconnects but not full host reboots. Quil survives reboots. It also understands pane types (a Claude Code pane resumes differently than an SSH pane), ships an MCP server for AI agents, and tracks per-pane notes alongside your work. For a deeper comparison, see /vs/tmux or /vs/zellij.",
  },
  {
    question: "Which AI tools does Quil support today?",
    answer:
      "Claude Code has first-class support via the built-in Claude Code pane type, with auto-resume on daemon restart, a setup dialog that pre-fills the active pane's working directory (so the project's `.claude/` context is preserved), and a one-click `Dangerously skip permissions` toggle for unattended runs. Quil also runs an MCP server (`quil mcp`) that exposes 15 tools so any MCP-capable client can read pane output, send keystrokes, and snapshot a workspace. Any other AI tool can be wrapped in a custom TOML plugin that defines its spawn command, resume strategy, and error patterns.",
  },
  {
    question: "Can I paste a screenshot into Claude Code on Windows?",
    answer:
      "Yes. Quil ships a Win32 clipboard image proxy that works around the upstream Claude Code Windows clipboard image bug (anthropics/claude-code#32791). Take a screenshot with Win+Shift+S, focus a Claude Code pane, and press F8 (or Ctrl+Alt+V — Windows Terminal eats Ctrl+V before it reaches the TUI). Quil decodes the clipboard DIB, saves a PNG under `~/.quil/paste/` with owner-only 0o600 permissions and a crypto/rand filename suffix, then types the absolute file path into the pane. Claude Code reads the file via its normal file-reading tools.",
  },
  {
    question: "Does Quil work on Windows without WSL?",
    answer:
      "Yes. Quil ships a native Windows binary that uses ConPTY for pseudo-terminal support and Named Pipes for client-daemon IPC. No WSL required. Linux and macOS use creack/pty and Unix domain sockets, respectively.",
  },
  {
    question: "How does the reboot-proof persistence actually work?",
    answer:
      "Quil runs as a client-daemon pair. The daemon (quild) continuously snapshots workspace state to ~/.quil/workspace.json and maintains 500-line ghost buffers per pane in a SQLite database. On reboot the client spawns the daemon, reads the snapshot, re-creates the pane split tree, and replays ghost buffers instantly while shells re-initialise in the background.",
  },
  {
    question: "Can I write my own plugins?",
    answer:
      "Yes. Plugins are single TOML files in ~/.quil/plugins/<name>.toml with sections for spawn, resume, keybindings, error handlers, and status lines. No compilation, no restart, hot-reload on save. See the plugin reference on GitHub or the /plugins page for a walk-through.",
  },
  {
    question: "What happens when I upgrade Quil and plugin configs have changed?",
    answer:
      "Quil detects when your on-disk plugin TOML has a lower schema_version than the version shipped with the new binary. Instead of silently overwriting your config, it opens a full-screen side-by-side merge dialog at startup: your config on the left (editable), the new default on the right (read-only). Lines unique to your config are highlighted red, new lines in the default are highlighted green. Copy what you need from the right, edit on the left, then Ctrl+S to save. You can also press F5 to accept the new default entirely. The dialog blocks until resolved — no risk of running with a stale config.",
  },
  {
    answer:
      "Yes. Quil is open source under the MIT License. There's no hosted version, no paid tier, no telemetry. You self-host it on your own machine and it stores all state locally under ~/.quil/.",
  },
  {
    question: "How do I install it?",
    answer:
      "On Linux or macOS: `curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh`. Go users can `go install github.com/artyomsv/quil/cmd/quil@latest`. Windows users download the .zip from the latest GitHub release. Full instructions at /install.",
  },
];
