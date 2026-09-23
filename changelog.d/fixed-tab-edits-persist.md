---
headline: Tab renames and colours survive a daemon restart
---
- **Renaming or recolouring a tab is saved immediately.** It used to wait for the
  30-second periodic snapshot, so a daemon stopped inside that window came back
  with the old name and colour.
