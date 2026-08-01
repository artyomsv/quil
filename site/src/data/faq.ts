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
    question: "Can I use Quil on a remote server over SSH?",
    answer:
      "Yes — `quil --remote gpu01` runs the daemon on the server and the TUI on your laptop, so the panes and AI sessions live on the remote host and keep running when you close the lid. No port is opened on the server: Quil runs `ssh -T gpu01 \"quil --stdio\"` and speaks its normal protocol over that one channel, so bastions behind ProxyJump, Tailscale addresses, hardware tokens, and your existing ~/.ssh/config all work unchanged. If the link drops, an amber bar names the host and reconnects on its own with backoff — nothing stopped running, so there is nothing to resume. The server does not need Quil installed first: point --remote at a bare machine and it downloads the release for the remote's platform onto your laptop, verifies the checksum there, and pushes it over the connection you already have. It is beta today — plugin definitions still come from your local machine, and remotes must be Linux or macOS.",
  },
  {
    question: "How do I find the right pane when I have dozens open?",
    answer:
      "Press Alt+Shift+P for the command palette. It fuzzy-finds every pane and tab across the whole workspace (listed as tab.pane with the pane type, so duplicates are distinguishable) and every action, each row showing its keybinding. The same box also searches content: start typing and a `Found in panes` section lists every pane whose scrollback contains your text, with a match count and a preview of the most recent hit — press Enter on one to jump straight to that pane. That answers \"which pane had that error, URL, or container id?\" without hunting tab by tab. No other multiplexer searches across panes without a third-party plugin.",
  },
  {
    question: "Which AI tools does Quil support today?",
    answer:
      "Claude Code has first-class support via the built-in Claude Code pane type, with auto-resume on daemon restart, a setup dialog that pre-fills the active pane's working directory (so the project's `.claude/` context is preserved), and a one-click `Dangerously skip permissions` toggle for unattended runs. Quil also runs an MCP server (`quil mcp`) that exposes 18 tools so any MCP-capable client can read pane output, send keystrokes, snapshot a workspace, and query per-pane memory usage. Any other AI tool can be wrapped in a custom TOML plugin that defines its spawn command, resume strategy, and error patterns.",
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
    question: "Why does the Windows build include OpenConsole.exe?",
    answer:
      "On Windows 10, the built-in console host (conhost.exe) mis-renders some TUIs — Claude Code's input box, for example, shows an extra space after the first typed character. Quil fixes this automatically by bundling Microsoft's MIT-licensed OpenConsole (the same console host Windows Terminal ships) and hosting panes through it; on Windows 10 it is extracted to ~/.quil/conpty/ at first run. Windows 11's console host is already correct, so nothing is extracted there. Attribution and license text are on the /legal page and in THIRD_PARTY_LICENSES.md.",
  },
  {
    question: "How does the reboot-proof persistence actually work?",
    answer:
      "Quil runs as a client-daemon pair. The daemon (quild) continuously snapshots workspace state to ~/.quil/workspace.json (atomic temp+rename) and maintains 500-line ghost buffers per pane as binary files under ~/.quil/buffers/. On reboot the client spawns the daemon, reads the snapshot, re-creates the pane split tree, and replays ghost buffers instantly while shells re-initialise in the background.",
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
    question: "What if Quil hangs or a pane stops responding?",
    answer:
      "One command recovers everything: `quil restart` stops the daemon with escalating force (graceful shutdown with a final state snapshot, then SIGTERM, then force-kill — each step has a timeout, so even a fully deadlocked daemon can't resist), starts a fresh one, and reopens your tabs and panes from the last snapshot. AI panes resume their sessions. If only a single pane stops accepting input (for example an AI tool wedged mid-turn), Quil shows a 'Pane not accepting input' warning in the notification sidebar while every other pane keeps working — press Alt+R to restart just that pane in place.",
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
