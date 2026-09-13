# Code Review State: quil / warm-shell-pool

Last reviewed: 2026-09-13
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/H1] PowerShell relocation quoting didn't escape U+2018-201B smart quotes (measured RCE) — fixed 1dfebc4, verified against a real PowerShell tokenizer
- [code-quality/C1] warmPoolSession.Close() blocked the caller via unbounded Windows WaitExit, reached from spawnMu and ahead of pane-PTY teardown in Stop() — fixed 1dfebc4 (async retire(), Stop() bounded at stopTimeout)
- [code-quality/C2] claimed shell stayed at the pool's neutral 80x24, pane's real Cols/Rows discarded — fixed 1dfebc4 (Resize before the relocation write)
- [code-quality/I1] confirming OSC 7 was swallowed instead of replayed, leaving a warm-claimed pane's CWD blank until the next command — fixed 1dfebc4
- [code-quality/I2 + rules/#5] pool kept serving a stale shell after a plugin reload changed it, no guard — fixed 1dfebc4 (servesShell check in eligibility)
- [security/M2 + code-quality/S6 + rules/#7] pool built from shellinit.Configure's env only, never the plugin's own Env/RecordHistory/pane InstanceArgs — fixed 1dfebc4 (excluded from pooling rather than merged; also independently flagged by Greptile)
- [security/M1 + code-quality/I3 + rules/#8] warm_shell_pool_size had no upper bound — fixed 1dfebc4 (clamped to 8 in the pool constructor, logged when clamped)
- [code-quality/I4 + code-quality/I5 + security/L2 + rules/#2] fill phase had no read timeout; retry never backed off and logged every second including through ordinary shutdown — fixed 1dfebc4 (fillTimeout, exponential backoff capped at 30s, shutdown-log suppression)
- [qa/#2] empty-CWD claim guard had zero test coverage — fixed 1dfebc4 (added to TryClaimFailure table)
- [qa/#3] warmCWDMatches' EqualFold (Windows case-insensitivity) had zero test coverage — fixed 1dfebc4 (TestWarmCWDMatches_Case)
- [qa/#1 + rules/#3] the feature's own config->pool wiring in Start() was untested — fixed 1dfebc4 (extracted newShellPoolFor, TestNewShellPoolFor_ConfigAndRegistry)
- [security/L1] quoting test re-derived expected value with the same production expression — fixed 1dfebc4 (literal fixtures incl. smart-quote payload)
- [rules/#6] no daemon-lifecycle.md documentation for the new subsystem — fixed 41835c4

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/S1] double-wrapped warmPoolSession when a claimed session wraps an already-wrapped fill-time session — resolved as a side effect of the async-Close fix (nested wrapper is detected and joined rather than causing a duplicate WaitExit), not treated as a separate item
- [code-quality/S2] latent nil-channel deadlock in TryClaim's vacant send, reachable only via a hand-built pool bypassing the constructor (test-only shape) — not present via any production construction path (newWarmShellPool always sets both or neither); left as-is
- [security/L3] pool's neutral fill CWD is os.TempDir(), world-writable on multi-user Linux — accepted for this PR; revisit if the pool is ever extended to a more sensitive default
- [security/L4] `cd` without `--`, and unmeasured readline meta-key concern for non-UTF-8 locales — the OSC7 mismatch-then-reject already fails safe here; not blocking
- [code-quality/S3] pooled-spawn log line format differs from the cold path's (drops cmd=/args=) — cosmetic, deferred
- [code-quality/S4/S5] doc-comment gaps on os.TempDir()/80x24 rationale, and docs not mentioning fish/sh/cmd.exe get no pooling — partially addressed by the daemon-lifecycle.md addition; not re-litigating word-by-word
- [rules/#8] shellName duplication between warmshell.go and internal/shellinit (DRY) — low priority, deferred
- [rules/#9] pooled-claim log omits cmd=/args= (same as S3) — deferred
- [rules/#10] changelog fragment's latency sentence doesn't mention the failed-claim-timeout cost — deferred, minor wording
- [rules/#11] one test asserts "no retry yet" via a 30ms sleep margin — deferred, low flake risk given the 1s+ retry delay
- [qa/#5] unsupported-shell refusal (`default: return p`) has no direct unit test — deferred, unreachable via Start() today
- [qa/#6] 64 KiB unterminated-OSC bound has no test — deferred
- [qa/#7] one stop-race guard (successful-claim-return select) is masked by a redundant-by-design second drain() — deferred, not a real gap
- [qa/#9] a dropped `pending` field would hang a test rather than fail fast — deferred, test-quality nit

## Out of scope for this PR (tracked against issue #142, not this feature's review)
- Workspace-restore integration (parallel fill during respawnPanes)
- get_memory_report line for parked warm shells
- Pool rebuild (not just refusal) on MsgReloadPlugins
