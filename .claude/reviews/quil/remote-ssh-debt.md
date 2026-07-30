# Code Review State: quil / remote-ssh-debt

Last reviewed: 2026-07-30
Rounds completed: 1

Scope: PR #114, `git diff origin/master..HEAD` on `fix/remote-ssh-debt`.
Clears the three Medium SSH-transport techdebt items (RD-017…RD-019). Four
agents dispatched (security-officer, code-reviewer, rules-compliance, qa) plus
Greptile. **Security and rules reported; code-reviewer and qa never delivered a
report despite two direct requests** — their coverage was partly substituted by
work done in-session (mutation testing of every new TUI test, 6× flakiness runs
on `cmd/quil`, 10× native-Windows runs on `internal/transport`) and by
Greptile, which independently found the same finding class as the security
pass. Treat code-quality and test-design as UNDER-reviewed if a round 2 runs.

## Resolved (fixed in code; do not re-raise)

- [security/M1 + greptile/P1a] `ClassifyLinkFailure` decided on remote-influenced text — ssh multiplexes the REMOTE command's fd 2 onto its own stderr, and `permission denied` is a string any Unix shell emits, so an ordinary `~/.bashrc` touching an unreadable path would park the session and a compromised remote could do it deliberately. Gated on `!Established()`; the pump counts stdout only, so any byte proves ssh authenticated — round 1
- [greptile/P1b] The `Established()` gate alone left a path: ssh authenticates, the remote's rc files print the marker, the daemon produces no stdout, verify times out. Added `exitCode == ExitSSHOwnFailure` (255) — ssh passes the remote command's status through untouched, and a killed-not-exited ssh reports the kill, so a transient drop fails the gate too. Both gates moved INSIDE `ClassifyLinkFailure`; the raw match is unexported so no caller can use text alone — round 1
- [security/M2] The `quil.log` tee wrote sanitized-but-raw bytes; `terminalSanitizer` deliberately preserves `\n`, so a remote could emit a line byte-identical to a genuine slog record (rendered as one by the F1 viewer), and a chunk not ending in `\n` glued the next real record onto it. Framed through `sshStderrLogger` — split on `\n`, emit via `%q` — round 1
- [security/L1] Remote could roll the whole rotating-log archive set and evict local history. Session-length `sshStderrBudget` (256 KiB) with a single reported suppression — round 1
- [security/L2] `lockedBuffer` trimmed on every write past the cap: a remote pacing one byte at a time turned each byte into a 64 KiB alloc+copy. Trims at 2× the cap, amortized, same tail guarantee — round 1
- [security/informational] `lockedBuffer.buf` documented as NEVER embedded: embedding promotes `bytes.Buffer.ReadFrom`, so `io.Copy` takes the `ReaderFrom` fast path straight past both the mutex and the cap — round 1
- [rules/1 + security/L3] `10.0.0.1` (RFC1918) in `linkfailure_test.go` fixtures; the repo standardised on RFC 5737 after a prior round. Now `203.0.113.1`, matching `internal/config/remote_test.go` — round 1
- [self/CI] `changelog` check failed — production Go changes require an `## [Unreleased]` entry the release workflow copies verbatim. Added — round 1
- [self] `TestLockedBuffer_Write_KeepsOnlyTheTailOnceCapped` asserted `<= cap`, which the 2× trim does not guarantee; it passed only by luck of that write pattern. Corrected to the real bound, plus a one-byte-at-a-time case — round 1
- [self] `sshStderrLogger` shipped untested. Three tests added: newline escaping, partial-line holding, budget stop with a full write still reported — round 1

## Dismissed (acknowledged, will not fix)

- [semgrep] `math/rand` for backoff jitter — carried over from PR #113's dismissal; the jitter guards nothing (round 1)
- [rules/LOW] One 81-char line in RD-019's commit body vs the ~72 guidance. Amending three commits to rewrap prose rewrites history for no reader benefit (round 1)
- Residual on the classifier: a compromised remote that both exits 255 AND prints a marker can still force a park. Not meaningful — the IPC protocol is RCE-equivalent by design, so a compromised remote already controls the daemon (round 1)

## Notes for the next round

- **code-reviewer and qa produced no report.** The RD-017 descriptor-ownership
  change is the one with real deadlock/leak risk and never got an independent
  read: verify `c.r` is closed exactly once on every conn shape (real child,
  never-started, `newStdioConn(nil, …)`, close-before-output, double Close).
- Untested behaviour: `resumeReconnect`'s attempt preservation is asserted, but
  the classification call site in `cmd/quil/remote.go` is not covered
  end-to-end — only the pure classifier is.
- **Nothing here is verified on a real link.** Phase 2's outstanding manual
  checks still stand, plus two new ones: ssh diagnostics reaching `quil.log`
  after a reconnect, and a rejected key parking rather than retrying.
