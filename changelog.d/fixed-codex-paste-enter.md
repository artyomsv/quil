---
headline: Prompts delegated to a codex pane now actually start
---
- A prompt sent to a codex pane through `delegate_task`, `send_to_pane` with `paste`, or a workspace template's starting prompt could arrive complete and never run: codex ignores an Enter that lands within 120 ms of a pasted burst and folds it into the prompt as a newline, and Quil was sending it after 100 ms. The pane showed the prompt and sat idle with nothing reporting a failure. The gap is now 400 ms on both delivery paths.
