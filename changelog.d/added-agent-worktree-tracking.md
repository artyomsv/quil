---
headline: Quil notes worktrees and branches that agents create
---
- **Quil keeps a record of worktrees and branches that appear while agents
  run.** Claude Code, Codex and OpenCode tell Quil when an agent runs a
  command that can make or remove a worktree or a branch, including a plain
  `git worktree add` in the shell and Claude's own worktree tool. Quil then
  compares the repository's worktrees and branches with what it saw before,
  and also does that once at the end of every agent turn. Each new one is
  recorded with the pane it appeared in and the command that ran. Worktrees
  Quil made itself are not recorded. The record is the base for a cleanup view
  in the next release.
