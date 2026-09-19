---
headline: Closing your laptop no longer looks like a wedged daemon
---
- **A suspended machine no longer reports the daemon as wedged.** The snapshot
  watchdog measured staleness on the wall clock, which keeps running while a
  machine sleeps even though no ticker fires and no snapshot can complete. A
  laptop closed for ten minutes therefore produced `WATCHDOG: no snapshot
  completed for 10m9s — daemon may be wedged` and a full goroutine dump into
  `quild.log`, on a daemon that was working perfectly (reported in #221 on
  macOS, where the dump was mistaken for the cause of a separate problem).

  Staleness is now measured on the monotonic clock, which stops across suspend.
  A real wedge still dumps exactly as before.

- **The daemon log now says when a Claude hook was registered, not only when it
  failed.** All three refusal paths logged `claude hooks disabled`; a
  registration that succeeded logged nothing at all, so `grep -i hook
  quild.log` looked identical on a working install and on one where the hook
  had never been registered. A successful spawn now logs `claude hooks
  registered` with the settings path it wrote.
