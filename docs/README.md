# Quil Documentation

> Looking for the project landing page? See [../README.md](../README.md).
>
> Just installed Quil and want to use it? Start with [Quick start](quick-start.md), then read [MCP](mcp.md) to wire it into your AI assistant.

## Getting started

| Doc | What's in it |
|---|---|
| [Installation](installation.md) | One-line installer, Go install, manual download, build from source |
| [Quick start](quick-start.md) | First-launch walkthrough — five keys, opening a typed pane, persistence, AI connection |
| [Troubleshooting](troubleshooting.md) | Daemon won't start, MCP not detected, log file locations, force-stop, reset |

## Using Quil

| Doc | What's in it |
|---|---|
| [Features](features.md) | Capability tour — persistence, layout, typed panes, observability |
| [Keybindings](keybindings.md) | Full keymap, customization syntax, what to bind and what to leave for the PTY |
| [Configuration](configuration.md) | `~/.quil/config.toml` reference — every section + every key |
| [MCP](mcp.md) | **Let your AI assistant drive Quil.** Wiring for Claude Desktop / Claude Code / Cursor / VS Code Copilot + all 17 tools documented + redaction model |

## Customization

| Doc | What's in it |
|---|---|
| [Plugin reference](plugin-reference.md) | Author your own pane types in TOML — every field, every strategy, every example |

## Project

| Doc | What's in it |
|---|---|
| [Architecture](architecture.md) | All 24 ADRs — client-daemon split, IPC protocol, PTY cross-platform, persistence model, plugin system |
| [Vision](vision.md) | The project's why — what problem Quil solves and how |
| [Product requirements](prd.md) | Original PRD (historical reference) |
| [Roadmap](roadmap.md) | Milestone summary + planned work — see also [roadmap/](roadmap/) for per-feature PRDs |
| [Versioning](versioning.md) | SemVer policy and release process |
| [Contributing](../CONTRIBUTING.md) | Branch / commit conventions, how to submit changes |
| [Changelog](../CHANGELOG.md) | Per-release notes (auto-maintained) |

## Historical references

| Folder | Contents |
|---|---|
| [plans/](plans/) | Implementation plans for major features (M1 foundation, version negotiation, plugin migration dialog) |
| [roadmap/](roadmap/) | Per-feature roadmap PRDs — done items kept for context, planned items describe upcoming work |
| [superpowers/](superpowers/) | Detailed plans and specs from large feature efforts (e.g., memory reporting) |
