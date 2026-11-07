---
headline: Quil notes worktrees that appear while agents run
---
- **Quil keeps a record of worktrees and branches that appear while agents
  run.** Claude Code, Codex and OpenCode tell Quil when an agent runs a
  command that can make or remove a worktree or a branch, including a plain
  `git worktree add` in the shell and Claude's own worktree tool. Quil then
  compares the repository's worktrees and branches with what it saw before,
  and also does that once at the end of every agent turn. Each new one is
  recorded with the pane it appeared in. The command text is never stored,
  because it can hold a password or a token. Worktrees Quil made itself are
  not recorded. The cleanup view lists the record.
