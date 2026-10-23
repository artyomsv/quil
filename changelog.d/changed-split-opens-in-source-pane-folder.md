---
headline: A split terminal opens in the folder of the pane you split from
---
- A pane created with Ctrl+N and no folder of its own (Terminal, k9s, lazysql) now opens in the live directory of the pane you split from, as in tmux. It used to open in the folder quil was started from. If that folder is gone, or the pane is a sandbox, the project root is used.
