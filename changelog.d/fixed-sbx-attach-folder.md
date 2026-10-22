---
headline: Picking an sbx sandbox now opens the pane in its folder
---
- **Attaching a pane to an existing sbx sandbox picks a folder the sandbox
  holds.** The create dialog kept the folder you had browsed to, so a pane
  opened in a parent folder (the repository root, say) was refused at start
  with "is not in a writable folder of sbx sandbox". When ←/→ lands on a
  sandbox that does not list the current folder, the dialog now moves the
  folder to that sandbox's main workspace. You can still browse elsewhere
  before pressing Continue; Quil checks the folder again at start.
