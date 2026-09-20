# Hand-start interception list is frozen at daemon start

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/daemon/warmshell.go` (`newShellPoolFor`), `internal/daemon/daemon.go` (`handleReloadPlugins`) |
| Found during | Live testing of hand-started agent conversion, issue #221 |
| Date | 2026-09-20 |

## Issue

`newShellPoolFor` computes the interception name list once, from the registry as
it stands at `Start()`, and bakes it into every warm shell's environment as
`QUIL_INTERCEPT`. The list is not recomputed on `MsgReloadPlugins`, and existing
warm shells could not be updated if it were — their environment was fixed at
`exec`.

An agent installed *after* the daemon started is therefore never intercepted
until the daemon restarts. Typing it in a terminal pane simply runs it, exactly
as before the feature existed.

Observed directly: codex was installed at 17:32 against a daemon started at
17:27. `QUIL_INTERCEPT` read `[claude,opencode]`, and `codex` did not convert.
The daemon had logged `plugin "codex": "codex" not found on PATH` at startup,
but nothing connects that line to "and that is why typing codex did nothing".

## Risks

- Silent and self-inflicted: the user installs the agent Quil advertises support
  for, and the feature is simply absent with no message. That is the same shape
  as #221 itself — the daemon knows the answer and does not say it.
- The diagnosis is expensive. It took a log dive and a timestamp comparison to
  find, because nothing reports the armed set.
- It will recur for every user who installs a second agent later, which is the
  normal way people adopt them.

## Suggested Solutions

1. **Log the armed set at startup** (cheap, and the one that closes the
   diagnosis gap): one line naming the intercepted binaries and any known agent
   plugin that was excluded, with the reason. Turns a log dive into a grep.
2. **Rebuild the pool on plugin reload.** `handleReloadPlugins` already
   re-detects availability; rebuilding the pool there fixes shells minted
   afterwards. Existing warm shells keep the old list, so it is a partial fix —
   the pool has the same pre-existing limitation for shell config generally.
3. **Arm from a file the shell reads at each prompt** rather than from the
   environment, so the set can change without a respawn. Larger, and it trades a
   fixed environment for a per-prompt read; only worth it if 1 and 2 prove
   insufficient.
