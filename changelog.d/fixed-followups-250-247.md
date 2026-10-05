---
headline: Shared-workspace sign-in and viewer handling made stricter
---
- **An agent's `close_tui` or `set_active_pane` no longer lands on a read-only viewer.**
  With no client named, the daemon picks the window someone last typed in, and it now
  skips viewers when it picks. A new pane's default folder skips them too. Naming a
  viewer's client id still reaches that viewer.
- **A client can no longer attach with a client id that starts with `reserved-for-local:`.**
  The daemon uses that marker for a reserved size-master slot, and a client wearing it
  read itself as the master.
- **The TCP listener is stricter.** It refuses a second start and a start after the
  daemon stopped, and it checks that the address is a loopback IP before it binds. A
  flood of connections past the pending-login limit now writes one line per minute to
  `quild.log`, not one line per connection.
