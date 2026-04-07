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
      "Claude Code has first-class support via the built-in Claude Code pane type, with auto-resume using `claude --resume <session-id>`. Any other tool can be wrapped in a custom TOML plugin that defines its spawn command, resume strategy, and error patterns. Cursor integration is on the roadmap.",
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
    question: "Is Quil free?",
    answer:
      "Yes. Quil is open source under the MIT License. There's no hosted version, no paid tier, no telemetry. You self-host it on your own machine and it stores all state locally under ~/.quil/.",
  },
  {
    question: "How do I install it?",
    answer:
      "On Linux or macOS: `curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh`. Go users can `go install github.com/artyomsv/quil/cmd/quil@latest`. Windows users download the .zip from the latest GitHub release. Full instructions at /install.",
  },
];
