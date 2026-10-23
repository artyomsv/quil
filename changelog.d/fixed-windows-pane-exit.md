---
headline: Windows panes now notice when their program exits
---
- **On Windows, a pane now notices when its program exits.** The console stayed open after
  the program ended, so the pane never learned about the exit: no "Process exited" card, no
  return to the shell for a hand-started agent, and an overlay that was quit stayed in its
  slot. Quil now closes the console when the program exits, the way Windows Terminal does,
  and the program's last output still reaches the pane.
