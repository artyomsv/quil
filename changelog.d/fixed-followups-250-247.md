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
- **A confirmed note reload (Ctrl+R twice) now loads the daemon's text after a daemon crash.**
  A daemon that stopped just after a note save can come back with an older note version
  than the open editor holds, and the reload was dropped without a word. Now it loads.
  If you typed after you confirmed, an older reload is still dropped, and the status bar
  says so. A background reload that is older than the editor is dropped without a word,
  as before: the editor already holds the newer text.
- **A note file that cannot be read is offered again on the next launch.** Before, one
  failed read marked the old notes as imported for good.
- **A reconnect gives the one-time import of old files its full retries again.** Before,
  error replies were counted for the whole session, so after a reconnect one more error
  stopped the import.
- **Windows: the daemon no longer rewrites the access list of a folder that is not Quil's.**
  If `QUIL_HOME` points at a folder that also holds your own files (hidden ones such as
  `.git` included), the daemon leaves its access list alone, writes a warning to `quild.log`, and does not start the TCP listener.
  The daemon also reads the quil folder's access list first, and when it is already
  owner-only, it no longer rewrites the whole folder tree on every start.
- **One host can add at most 64 groups to the sidebar.** The cap already applied to the
  group list a host sends, but a project filed under a name the host did not list added
  a group too, and a new set of names in each update kept growing the sidebar and the
  saved groups file. Now a host's project joins a group only when that host lists the
  name, or the group is your own. The groups file remembers which host added each group,
  so this also holds after a restart, and a host's empty group goes away when the host
  stops listing it. Names that differ only in spaces, case or length past 32 characters
  are one group. A project that cannot join is shown ungrouped, and `quil.log` says so
  once.
- **A long status-bar message is now shown, not hidden.** When a message did not fit
  beside the key hints, the status bar dropped the hints and the message together, so a
  refused create said nothing. Now the message comes first and the hints make room.
- **A host that is disconnected now refuses new panes at once.** When a host's link is
  down (for example, its token was revoked), Ctrl+N and Ctrl+T on its projects show
  "pane not created: <host> is disconnected — <reason>" in the status bar. Before, the
  form opened and the new pane waited for an answer that never came. You can still look
  at the host's panes.
- **You can no longer disconnect the host a window was started on.** With `--connect`
  or `--remote`, "Disconnect host…" on that host's projects is greyed out, and the
  palette says why. Before, it removed every project and left the window on
  "Connecting to quild…" for good. Other hosts still disconnect, also from a
  read-only connection.
