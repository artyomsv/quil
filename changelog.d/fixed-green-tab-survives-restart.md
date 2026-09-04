---
headline: Green finished-while-away tabs survive a restart
---
- **The green "finished while you were away" tab survives a restart.** The mark
  lived only in the TUI's memory, so every restart dropped it and the attach
  replay could only rebuild the few whose start edge was still in the event
  queue. The daemon now keeps a copy the TUI reports on every set and clear,
  persisted in the workspace snapshot, and a fresh TUI seeds each pane from it
  once before the replay refines it. Looking at the pane still clears it, and
  that clear is remembered too.
