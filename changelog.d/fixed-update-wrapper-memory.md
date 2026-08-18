---
headline: Updating in-session no longer strands a full copy of the session
---
- **A quil left running after an in-session update no longer holds the whole finished
  session in memory.** Applying an update relaunches quil and keeps the old process alive
  behind it, holding the terminal so your shell does not print a prompt over the new one.
  That process had just run a full session, so it parked still holding every pane's
  terminal emulator and scrollback — measured at 326 MB and 436 MB on a machine that had
  updated twice without quitting.

  It now releases that memory back to the operating system before parking. The processes
  themselves are unchanged and still exit when you quit quil.
