---
headline: Accepting a remote upgrade now reconnects the host
---
- **Accepting the remote-upgrade offer no longer leaves the host with no daemon.** The
  push does its job — new binaries, and the remote daemon stopped so it can restart on
  the new ones — but the message saying it had finished was filtered against a field only
  the New Project dialog ever sets, so the offer's own completion matched nothing and was
  discarded. Since the only thing that starts a remote daemon is a dial, and nothing
  dialled, the host was left updated, daemonless, with its panes stopped and the project
  still reading "upgrading…". It now hands the host back to the reconnect ladder, which
  dials it, starts the daemon, and restores the panes from the snapshot the old daemon
  wrote on its way out.

- **A push that fails says so.** It reported nothing at all before, for the same reason;
  the project now names the command that finishes the job by hand.
