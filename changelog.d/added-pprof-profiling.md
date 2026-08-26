---
headline: Set QUIL_PPROF to profile a running quil or quild
---
- **Both binaries can serve Go pprof profiles on demand.** Set `QUIL_PPROF` to a
  port before launching, and that process exposes CPU, heap, goroutine and
  allocation profiles for as long as it runs:

  ```
  QUIL_PPROF=6060 quil          # the TUI
  QUIL_PPROF=6061 quild         # the daemon needs its own port
  ```

  Start `quild` first if you want both — the TUI auto-starts the daemon and
  passes its environment down, so a daemon that inherits the TUI's port just
  logs `address already in use`.

  Nothing listens when the variable is unset — no port, no goroutine, no cost.
  When it is set, the listener binds loopback only and refuses to start on any
  other address: a bare port number becomes `127.0.0.1:<port>`, and an explicit
  non-loopback host is rejected with an error rather than bound.

  It is **not authenticated**, and loopback is a machine boundary rather than a
  user boundary — while the port is open, any account on that machine can read
  the profiles. Profiles do not contain terminal buffer contents, but the
  command line (which for `quil --remote` names the destination host) and full
  goroutine stacks are among them. Set it for an investigation rather than
  leaving it in a shell profile.

  `scripts/pprof.sh <port> [profile] [seconds]` fetches one and renders it, and
  `scripts/pprof-view.sh` re-examines a saved profile without re-sampling.
  `docs/troubleshooting.md` has the full walkthrough.
