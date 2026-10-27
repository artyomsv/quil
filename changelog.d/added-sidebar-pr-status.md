---
headline: The sidebar shows each pane's pull request and its status
---
- **Each pane's git row in the sidebar now ends with its pull request, like `#277`.** The
  number is colored by state: grey while open, dim for a draft, grey with `…` while CI runs,
  green when approved, orange when it needs you (a red check, changes requested, or a merge
  conflict), red when closed without merging, light blue when merged. Quil asks `gh pr view`
  for the pane's branch when the branch changes and then every two minutes; with no `gh`, no
  login, a non-GitHub remote or no PR for the branch the row looks as before. When the row is
  tight the `↑N`/`↓N` counts give way before the chip, which is never cut in its digits. Turn
  it off with `[daemon] pr_status = false` (also in F1 → Settings).
