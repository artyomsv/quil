---
headline: The sidebar shows each pane's pull request and its status
---
- **A pane whose branch has a pull request gets a row under its git row in the sidebar, like
  `#278 approved`.** The number and the state as a word, colored by state: grey open, dim
  draft, grey `running` while CI runs, green `approved`, orange `attention` when it needs
  you (a red check, changes requested, or a merge conflict), red `closed` (closed without
  merging), light blue `merged`. Quil asks `gh pr view` for the pane's branch when the branch
  changes and then every two minutes; with no `gh`, no login, a non-GitHub remote or no PR
  for the branch there is no row. Turn it off with `[daemon] pr_status = false` (also in
  F1 → Settings).
