---
headline: A new terminal tab no longer inherits the last pane's sandbox
---
- A plugin with no setup step (Terminal) skipped the create dialog's sandbox reset, so a shell tab opened after an sbx pane was sent into that pane's sandbox and refused with "is not in a writable folder of sbx sandbox". The sandbox row is now cleared on every plugin pick.
