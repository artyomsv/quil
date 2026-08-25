---
headline: Mark a pane for deletion when you are done with it
---
- **Mark a pane for deletion.** A new context-menu row, beside **Mark attention**,
  puts a red `⌫` on a pane to record that you are finished with it and it is safe to
  close. It is for the pane you deliberately keep alive — a deployment still running,
  a server you want to test against — so that when you come back to it you can close
  it on sight instead of reading its scrollback to work out whether it still matters.

  The mark shows in the sidebar pane row, as a `⌫N` count on the project row, as a
  prefix on the tab label, and as a red pane border. Like the attention pin it is
  stored on the daemon, so it survives a TUI restart, a daemon restart and a reboot,
  and reads the same in every client attached to that daemon. It survives a pane that
  is still busy too: when a live state claims the row's glyph the mark moves to the
  end of the row in its own colour rather than disappearing, which is the whole point
  — the pane you marked is usually the pane still doing something.

  It is **mutually exclusive with Mark attention**: the two say opposite things about
  a pane, so setting either clears the other. The daemon enforces that, so the answer
  is the same in every client. Unmarking one leaves the other alone.

  Unlike the tab bar's blocked, pinned and unseen marks, a deletion mark does not
  recolour the tab — a pane you have already decided to throw away is the least
  urgent thing in the workspace and must not compete with the three states that
  actually want you to act.
