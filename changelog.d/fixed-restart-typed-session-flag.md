---
headline: Restarting an agent pane reopens its own conversation
---
- **Alt+R no longer opens another pane's conversation.** A pane that started
  as `claude --continue` typed in a terminal pane kept that flag for good, and
  a restart passed it next to `--resume <own session>`. Claude obeyed
  `--continue`, which opens the newest session in the folder, so with two
  panes in one folder the restarted pane joined the other pane's
  conversation and both wrote to one transcript. A restart now drops the
  typed session flag once the pane has recorded a session of its own, as a
  daemon restart already did, and keeps the other flags you typed. The same
  applies to `opencode --continue` and `codex resume <id>`.
