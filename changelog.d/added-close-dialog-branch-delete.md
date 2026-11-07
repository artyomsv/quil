---
headline: Closing a worktree pane lists the files it would lose
---
- **The close dialog shows what a worktree delete destroys, and can delete the
  branch too.** When you close a pane or tab whose worktree Quil created, the
  dialog lists the uncommitted, untracked and ignored files the delete would
  remove (10 rows, scroll with `PgUp`/`PgDn`). It can also delete the worktree's
  branch locally, and on its remote. Deleting the remote branch is off until you
  turn on **F1 → Settings → Allow remote branch delete**, and it then needs a
  typed `y`. The dialog warns about commits that exist only on the branch, and
  about an open pull request that the remote delete would close.
