---
headline: Clean up worktrees and branches agents left behind
---
- **A view to clean up worktrees and branches that agents left behind.** The
  command palette's **Worktrees from agents…** lists what Quil recorded while
  agents ran: each worktree or branch, the pane and agent it appeared under,
  its age, and whether a pane still uses it. Picking one opens the same
  confirm a pane close uses: it lists the files that would be lost and offers
  to delete the worktree, the local branch and (with the F1 switch on, and a
  typed `y`) the remote branch. Quil checks with git again before it deletes
  anything. `f` forgets an entry without touching git.
