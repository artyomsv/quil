---
headline: F1 Shortcuts shows its cursor and opens without leftovers
---
- **The Shortcuts list shows which row you are on.** `↑`/`↓` moved an invisible
  cursor, so the only sign a key had done anything was the whole list sliding once
  the cursor reached the edge of the window. The row under the cursor is now marked
  `>` and drawn bright, like every other list in Quil.
- **F1 → Shortcuts and F1 → Processes no longer open over the leftovers of the menu
  they came from.** Both are drawn in a wider box than the About menu, and neither
  asked for a repaint — so the old menu's border was left standing inside the new
  box until something else forced a full redraw, such as clicking away and back.
  Leaving Shortcuts with `Esc` repaints for the same reason.
