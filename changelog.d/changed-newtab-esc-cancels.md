---
headline: Esc in the new-tab picker cancels instead of making a tab
---
- **`Esc` in the new-tab picker now cancels.** `Ctrl+T` opens a picker asking which
  pane the tab should start with, and pressing `Esc` there closed the picker and
  created a plain terminal tab anyway — there was no way to change your mind, and
  the one key that means "back out" everywhere else in Quil was the one key that
  could not back out of this. The picker's own footer had been offering
  `Esc cancel` the whole time.

  It now closes the picker and creates nothing. The old two-keystroke path to a
  shell tab is `Ctrl+T` `Enter` `Enter`: Terminal is the first category in the
  list, so the shell is still two keys away from the picker being open.
