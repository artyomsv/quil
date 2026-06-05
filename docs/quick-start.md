# Quick Start

Get from `quil` to a productive workspace in ~3 minutes.

## Table of contents

- [Before you start](#before-you-start)
- [Launch](#launch)
- [The five keys you need](#the-five-keys-you-need)
- [Open your first typed pane](#open-your-first-typed-pane)
- [Save your workspace](#save-your-workspace)
- [Connect an AI assistant](#connect-an-ai-assistant)
- [Where to go next](#where-to-go-next)

## Before you start

You need:
- `quil` and `quild` installed and on your `PATH` — see [Installation](installation.md)
- A terminal emulator (Windows Terminal, iTerm2, Alacritty, Kitty, GNOME Terminal — anything supporting 256 colours)

Optional but recommended:
- An AI assistant that speaks MCP (Claude Desktop, Claude Code, Cursor, VS Code Copilot Chat) — see [MCP](mcp.md)

## Launch

```bash
quil
```

The first launch shows a beta disclaimer with a tip. Press **Enter** to continue, or press **→ Enter** on `Don't show again` to skip it next time.

The TUI auto-starts the daemon (`quild --background`) if it isn't running. You'll land in a single tab named `Shell` with a single terminal pane in your current directory.

## The five keys you need

Don't memorize the [full keymap](keybindings.md) yet. Five keys handle 90% of day-one usage:

| Key | What it does |
|---|---|
| `F1` | About menu → Settings, Plugins, log viewers, Memory |
| `Ctrl+N` | New typed pane (Claude Code, OpenCode, terminal, …) |
| `Ctrl+T` | New tab |
| `Ctrl+W` | Close active pane |
| `Ctrl+Q` | Quit (workspace persists — re-launch picks up where you left off) |

You can navigate panes with `Alt+Arrow` (spatial — left/right/up/down focus the closest pane in that direction). Click any pane with the mouse to focus it.

## Open your first typed pane

Press `Ctrl+N`. The plugin picker opens with categories:

- **Terminal** — system shell
- **AI Assistant** — Claude Code, OpenCode (beta)
- **Remote** — SSH (POC)
- **Tools** — Stripe (POC)

Pick **AI Assistant → Claude Code** (if you have `claude` on your `PATH`). The setup dialog asks for:

- A **working directory** — the active pane's CWD is pre-filled. Tab/arrows navigate, Enter descends, Backspace goes up.
- A **permission mode** toggle — two mutually-exclusive radio buttons:
  - `--dangerously-skip-permissions` (unattended)
  - `--enable-auto-mode` (safer alternative)
  - (Pick neither for the standard interactive flow)

Finish the dialog. Quil spawns Claude Code with `--session-id <new-uuid>` so the session is uniquely addressable, and the pane fills the tab.

## Save your workspace

You don't have to. Quil snapshots automatically:

- 500 ms after structural changes (pane create/destroy, tab switch, layout change)
- Every 30 s as a safety net
- Once more on clean shutdown

The state lives in `~/.quil/workspace.json` (atomic temp+rename). Per-pane scrollback is in `~/.quil/buffers/<pane-id>.buf`. On next launch, your tabs, layouts, working directories, plugin types, and even ghost previews of the pane content come back instantly.

Want to confirm? Quit with `Ctrl+Q`, re-launch with `quil`, and you'll see your panes restored — the AI pane will run `claude --resume <session-id>` automatically so the conversation continues where it stopped.

## Connect an AI assistant

This is the differentiator. Once your AI tool can call into Quil over MCP, it can see your build pane output, send commands, watch for events, and orchestrate your workspace.

The shortest path: add this to `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or your AI client's MCP config:

```json
{
  "mcpServers": {
    "quil": {
      "command": "quil",
      "args": ["mcp"]
    }
  }
}
```

Restart the client. Then ask the AI:

> List all my Quil panes.

You should see a JSON array of every pane with its id, type, tab, and CWD. If you don't, see [MCP → Troubleshooting](mcp.md#troubleshooting).

The full [MCP guide](mcp.md) covers all 17 tools, wiring for Claude Code / Cursor / VS Code, the redaction model for secrets, and example prompts.

## Where to go next

- [Features](features.md) — every capability in one tour
- [Keybindings](keybindings.md) — the full keymap
- [MCP](mcp.md) — let your AI assistant drive Quil
- [Configuration](configuration.md) — `~/.quil/config.toml` reference
- [Plugin reference](plugin-reference.md) — author your own pane types in TOML
- [Troubleshooting](troubleshooting.md) — daemon won't start, MCP not detected, log file locations
