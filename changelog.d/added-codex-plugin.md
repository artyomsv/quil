---
headline: Codex panes with notifications, work state and resume
---
- **Codex (OpenAI's coding agent CLI) is a built-in pane type.** `Ctrl+N` → Codex
  opens it in a folder of your choice, with setup toggles like Claude Code's:
  bypass approvals and sandbox, or auto workspace-write; and web search.

  Quil registers its hook with codex per pane — a `-c hooks=…` override that
  carries its own trust hash, so nothing under `~/.codex` is touched and no trust
  prompt appears — and everything the Claude Code plugin derives from hooks works
  for Codex too: the notification sidebar (permission asks, "reply ready",
  compaction), the work-in-progress spinner and green/amber tab marks, subagent
  tracking, the model and context-token status segment, `Alt+Shift+I` input
  history, and per-pane session resume after a daemon restart (`codex resume <id>`;
  a pane with no recorded session starts fresh, never on a sibling's conversation).

  Tier knob: `[notification.hooks] codex = "default" | "verbose" | "off"`.
