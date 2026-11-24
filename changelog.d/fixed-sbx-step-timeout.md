---
headline: Sandbox setup steps stop when their time runs out
---
- **An sbx sandbox's setup steps now stop when their time runs out.** A host step
  (`prepare_host`) or an sbx command that started another process holding its
  output could keep the pane waiting well past the step's budget, for as long as
  that process lived, because only the command itself was stopped. A step that
  runs out of time now stops with everything it started (on Windows, the command
  itself), and returns at once.

  A step that finishes but leaves a process running that still holds its output no
  longer keeps the pane waiting either: Quil stops reading after two seconds and
  notes it in the pane.
