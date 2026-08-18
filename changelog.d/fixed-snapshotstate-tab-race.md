---
headline: Two clients attaching at once can no longer race the daemon
---
- **The daemon no longer races itself when two clients attach at the same moment.**
  `SnapshotState` promises callers a consistent view of the session taken under one lock,
  and delivered that for projects but not for tabs — those came back as live pointers, so
  anything reading a tab's pane list after the lock was released raced a concurrent pane
  creation.

  Each connection is dispatched on its own goroutine, so two clients attaching together was
  enough to hit it: one builds the default workspace while the other builds the workspace
  state it is about to be sent. Tabs are now copied on the way out, exactly as projects
  already were.
