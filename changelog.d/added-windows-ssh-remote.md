---
headline: Control a Windows PC's quil over ssh with quil remote setup
---
- Control a Windows machine with `quil --remote <host>` over its built-in OpenSSH server. The daemon now survives ssh disconnects, and `quil daemon install-logon` makes it start in your desktop session. A daemon started over ssh without that task runs with normal (never admin) rights and shows `[limited]`.
- `quil remote setup` installs and upgrades quil on Windows hosts.
- Windows pane folders now show correctly on Linux and macOS clients.
