---
headline: The sidebar shows the branch of a folder that holds checkouts
---
- **A pane in a folder that holds repositories shows their branch.** A folder
  that is not a checkout itself but groups several as direct children — a
  ticket's worktrees side by side, say — used to get no git row at all. The
  sidebar now looks one level down: when every child checkout is on the same
  branch the row shows that branch (ahead/behind summed across them, the
  worktree marker when all are worktrees), and when they disagree it shows
  `N repos` instead. Hidden folders and links are skipped, a folder with more
  than 16 subfolders is left alone, and each child is probed exactly as a
  pane's own checkout is — one git call per child at most, and none while
  nothing has changed.
