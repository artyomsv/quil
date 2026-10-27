---
headline: An sbx sandbox pane shows the work spinner and notifications
---
- A Claude pane in an sbx sandbox now runs Quil's hooks: the work spinner, notifications, input history and `/clear` tracking work as on the host. Quil shares two folders of its own with the sandbox (`~/.quil/sandbox/sbx/<sandbox>` read-write, the Linux hook binary read-only). Do not delete that folder while the sandbox exists; see docs/sandbox-panes.md.
