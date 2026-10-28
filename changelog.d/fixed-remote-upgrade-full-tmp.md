---
headline: A failed remote upgrade no longer stops the daemon there
---
- **A remote upgrade copies the new version before it stops the remote daemon.**
  The order was stop, then copy. Almost every failure is in the copy (a full disk
  or quota, a dropped link), so a failed upgrade stopped the daemon and killed what
  its panes were running, then installed nothing — once per retry. The install is
  now three steps: copy and unpack next to the install, stop the daemon, then swap
  the files in by rename. A failed copy leaves the host exactly as it was.
- **A remote install no longer needs room in the host's `/tmp`.** The archive was
  unpacked in `/tmp`, which is often a small tmpfs with a per-user quota. A host
  whose `/tmp` was full failed every upgrade with `cat: write error: Disk quota
  exceeded` while its home had plenty of space. The copy now goes into the install
  directory itself, as the Windows install already did.
- **A failed remote upgrade says why in the pane.** It showed only "upgrade failed —
  run: quil remote setup …"; the reason was in `quil.log` and nowhere else.
