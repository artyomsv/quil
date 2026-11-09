---
headline: Closing a worktree pane lists the files it would lose
---
- **The close dialog shows what a worktree delete destroys, and can delete the
  local branch too.** When you close a pane or tab whose worktree Quil created,
  the dialog lists the uncommitted, untracked and ignored files the delete
  would remove (10 rows, scroll with `PgUp`/`PgDn`, `←`/`→` between worktrees).
  A second row deletes the worktree's local branch as well, and warns about
  commits that exist only on that branch. Quil never deletes a branch on a
  remote. A worktree whose files could not be read is never deleted, nor a
  folder that is no longer the checkout Quil made; a sidebar card says why
  whenever a worktree you asked to delete is kept.
