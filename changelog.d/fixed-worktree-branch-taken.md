---
headline: New tabs say when a worktree is still being created
---
- **A new tab created on a new branch now says what it is waiting for, and says
  when it failed.** Opening a tab with an agent on a fresh worktree used to show
  a shell prompt in the repository root while `git worktree add` ran — which on
  a large monorepo is minutes, and is indistinguishable from a create that
  finished and put you in the wrong tree. The tab now holds a placeholder that
  names the branch being checked out, with a spinner that keeps running for as
  long as the checkout does.

- **A worktree that could not be created explains itself in the pane.** The only
  notice used to be a three-second message in the status bar, so a create that
  git refused left a tab that looked like it had worked and an agent that never
  arrived. The pane now shows git's own reason and offers `Alt+R`.

- **The pane setup dialog refuses a branch name that already exists**, instead of
  sending a create that cannot succeed. It could not see such a name before: the
  dialog knew only the branches that have a worktree, so the common case — a
  branch whose worktree was removed earlier — was invisible until git refused it.
