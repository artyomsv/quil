---
headline: Stray text like 5;90;35M no longer appears in panes
---
- **Mouse moves no longer type stray text like `5;90;35M` into a pane.** While the
  project sidebar is shown, Quil asks the terminal to report every mouse move. When
  one report arrived in two parts more than 50 ms apart — usually on a busy machine —
  the first part was dropped and the rest was read as typed keys, which Quil sent to
  the active pane. Quil now recognises the rest of a broken mouse report and drops it;
  a real key press in between is still typed as normal.
