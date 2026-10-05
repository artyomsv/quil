---
headline: Stricter shared workspaces; refused creates now say why
---
- **An agent's `close_tui` or `set_active_pane` no longer lands on a read-only viewer.**
  With no client named, the daemon picks the window someone last typed in, and it now
  skips viewers when it picks. A new pane's default folder skips them too. Naming a
  viewer's client id still reaches that viewer.
- **A client can no longer attach with a client id that starts with `reserved-for-local:`.**
  The daemon uses that marker for a reserved size-master slot, and a client wearing it
  read itself as the master.
- **The TCP listener is stricter.** It refuses a second start and a start after the
  daemon stopped, and it checks that the address is a loopback IP before it binds.
  `quild.log` no longer fills up when a local program opens connections in a loop: a
  connection that never signs in writes nothing at the normal log level, and connections
  refused past the pending-login limit write one line per minute, not one per connection.
- **A damaged `tokens.json` entry is named in `quild.log`.** A sign-in with a token whose
  stored keys cannot be read is still refused with "token refused", and the daemon log now
  says which token is damaged, at most once a minute. When two entries in the file have
  the same id, the first one is kept, and the second one is counted in the "unreadable
  entries skipped" warning.
- **A pane or tab the daemon refuses to create now says why.** This happens when the
  daemon does not know a toggle or kube context the dialog sent (for example, after a
  plugin file was edited and the daemon was not reloaded). The status bar shows the
  daemon's reason, the empty split slot goes away, and a pane you chose to replace comes
  back as it was.
- **`quil clients token list` has an EXPIRED column.** An expired token stays in the list
  until you revoke it; the column shows `yes` for it.
- **Under `--connect`, the TUI checks your token's rights from the first second.** Before
  the first workspace update arrived, the create-pane dialog read a `standard` token as
  `full` and offered plugin instances it then could not start.
- **Reconnecting a `--connect` session checks the daemon's version.** A reconnect to a
  daemon that now runs another version is written to `quil.log`. A destination that never
  attached is refused, as at launch.
- **Clearer texts.** `quil sandbox login` and `quil remote setup` name `--connect` when the
  session uses it, not `--remote`. A non-loopback address now reads "names ..., which is
  not loopback", which is also true for `--connect`, which does not bind.
- **A failed worktree for a new tab is reported only by the host that made it.** Two hosts
  with a branch of the same name no longer settle each other's new-tab request.
