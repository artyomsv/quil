# Quil

**The Persistent Workflow Orchestrator for AI-Native Development**

## 1. Executive Summary

Quil is a cross-platform terminal manager designed to eliminate "context loss." Unlike traditional terminal emulators or multiplexers (like tmux), Quil is **project-aware**. It doesn't just manage shells; it manages workflows by persisting the state of AI sessions, webhooks, and build tools even across full system reboots.

## 2. Core Architecture: "The Brain"

- **Language:** Go (Golang) for high-concurrency and easy cross-platform binaries.
- **Engine:** Internal Multiplexer (built via `creack/pty`). No dependency on tmux.
- **UI:** Bubble Tea for a rich, interactive TUI.
- **Model:** Client-Server Architecture.
  - `quild` (Daemon): A background server that maintains PTY sessions and monitors process output.
  - `quil` (Client): The frontend TUI that attaches to the daemon.

## 3. Key Feature Pillars

### A. Total State Persistence (Reboot-Proof)

- **Continuous Snapshotting:** Quil saves a `state.json` mapping of every Tab and Pane (Working Directory, Layout, Type, and Metadata).
- **Ghost Buffer:** Upon OS restart, Quil immediately renders the last 500 lines of cached text for every pane, providing instant visual context while the underlying shells re-initialize. **Status: Implemented** — ring buffer captures PTY output per pane; daemon replays to reconnecting clients.
- **Layout Persistence:** Pane layout (binary split tree) is serialized to JSON and stored in the daemon. On reconnect, the TUI restores the exact split configuration. **Status: Implemented.**
- **Process Re-hydration:** On startup, Quil doesn't just open a shell; it executes the Abstract Resume Command for that specific pane.

### B. Abstract Resume Engine

Quil uses a "Template & Scraper" model to ensure tools such as Claude Code or SSH sessions are never lost.

- **The Scraper:** A background regex listener that "watches" terminal output for Session IDs or Context Tokens (e.g., `Conversation ID: ([a-z0-9-]+)`).
- **The Template:** Users define an abstract command string per pane (e.g., `claude --resume {{.SessionID}}`).
- **Universal Support:** This allows Quil to "resume" any CLI tool (Claude, Gemini, Docker, SSH, etc.) without hardcoded logic.

### C. Typed Panes (Functional Workspaces)

Panes are assigned a Type with specialized behaviors:

- **AI Pane:** Optimized Markdown rendering and automatic session-id extraction.
- **Webhook Pane:** Real-time monitors (Stripe/Twilio). Borders flash Orange on activity or Red on errors. Supports auto-restart if the listener crashes.
- **Infrastructure Pane:** Displays persistent status lines (e.g., `K8s Context: production`) to prevent accidental commands.
- **Build Pane:** Integrated "Quick Actions" for Maven/Gradle/NPM. Tab colors reflect Success (Green) or Failure (Red).

### D. Advanced Layout & UI

- **Dynamic Positioning:** Tabs can be docked to Top, Bottom, Left, or Right.
- **Visual Logic:**
  - **JSON Transformer:** A hotkey (`Ctrl+J`) to toggle between Raw, Minified, and Pretty-Printed JSON with syntax highlighting.
  - **Pane Naming:** Every pane can be manually named (Alt+F2) or dynamically named based on the running process. **Status: Implemented.**
  - **Split-Views:** Support for infinite nesting of vertical and horizontal splits within a single tab. **Status: Implemented** — binary split tree (`LayoutNode`) with per-node split direction and ratio.
  - **Mouse Support:** Click to switch tabs and panes, scroll wheel for terminal history. **Status: Implemented.**
  - **Tab Colors:** Visual tab distinction with 8 color options, cycled via Alt+C. **Status: Implemented.**

### E. Automatic Shell Integration

- **OSC 7 CWD Tracking:** Quil auto-injects shell hooks (bash, zsh, PowerShell) at spawn time to emit OSC 7 escape sequences. The pane border displays the live working directory — no manual shell configuration required. Fish emits OSC 7 natively. **Status: Implemented.**

## 4. Technical Requirements

| Requirement          | Implementation Detail                                                                |
|----------------------|--------------------------------------------------------------------------------------|
| Persistence          | SQLite or JSON-based state storage in `~/.quil/`.                                   |
| Rendering            | GPU-aware via Windows Terminal or WezTerm as the host.                               |
| Shell Support        | Native ConPTY for PowerShell/CMD; PTY for Bash/Zsh.                                 |
| Syntax Highlighting  | Integrate Chroma for JSON/Code formatting.                                           |
| Networking           | Unix Sockets (Linux/Mac) and Named Pipes (Windows) for Client-Server communication. |

## 5. User Persona: The "AI-Native" Developer

The user is tired of re-typing `claude --resume` and re-opening five different project tabs every morning. They want to type one command — `quil` — and have their entire multi-tool environment snap back into existence exactly as it was when they went to sleep.
