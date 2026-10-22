---
headline: Switching to a project with an sbx pane no longer freezes quil
---
- **Switching to a project or tab whose sbx sandbox pane was not started yet no longer freezes the daemon.** After a restart, panes outside the active tab start on first visit. An sbx pane's first visit stood down while its sandbox was checked and announced that to every client — from inside the lock the lazy start holds — and the announce waited on that same lock, forever. Every later broadcast on the daemon waited behind it: existing panes showed "waiting for first output", a new tab or pane was never created, and a second TUI could not attach (`quil daemon status`: "running but not responding"). The deferred flag is read without that lock now.
