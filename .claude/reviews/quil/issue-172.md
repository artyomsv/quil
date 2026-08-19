# Code Review State: quil / issue-172

Last reviewed: 2026-08-19
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [code-quality/MEDIUM-3] The alt-screen override had no expiry and no repair path for key-less plugins. `altScreen` is sticky (a program killed without emitting rmcup never clears it) and `redrawKick`'s fallback for a plugin with no `redraw_key` is a resize jiggle a shell ignores — so `terminal`/`ssh` panes would have been blank on every reattach forever. Override now gated on a declared `redraw_key` — round 1
- [security/M1 + code-quality/MEDIUM-1] `pane.GhostSnap = nil` sat inside the `if ghostEnabled` block, so a pane that skipped its replay kept the restore snapshot — replayed by a later attach once the child left the alternate screen, painting a previous daemon session's screen into a live pane. Snapshot is now taken and cleared on the attach that finds it, whatever is done with it — round 1
- [security/LOW-1a] `scanMouseModes` stepped over an ESC that terminated a sequence, so `\x1b[?9\x1b[?1049h` hid an enable the terminal honoured — nine bytes of ordinary pane content. The ESC is now re-examined — round 1
- [security/LOW-1b,c] `switch string(param)` missed `?01049h` (leading zeros) and `?1049:1h` (sub-parameters), both of which reach the mode in every real terminal. Parameters are now parsed numerically with sub-parameters cut — round 1
- [security/informational + code-quality/MEDIUM-2] Alt-screen toggles triggered a full workspace broadcast and consumed the mouse-mode cooldown, for a field never sent to clients — and full-screen programs toggle it routinely, unlike the mouse modes the cooldown was designed around. `wireState()` excludes it from the trigger — round 1
- [security/LOW-2 + code-quality/LOW-4] `maxModeSeqLen`'s derivation said 29 bytes and cited 1049 as untracked; the real maximum is 42 with the alt-screen aliases. Comment corrected, and both carry-guard tests extended to the true maximum run — they had quietly stopped guarding it — round 1
- [code-quality/LOW-5] The integration test set `MouseModes.altScreen` directly, bypassing the scanner→attach seam. It now drives `flushPaneOutput` with a real enable sequence — round 1
- [qa] The rule file claimed the `ghostsnap` path was covered in general; it is covered for claude-code by `restoresOwnHistory` only. Wording corrected — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [qa] Restore replays a possibly-torn alt-screen snapshot for `ssh`/`stripe` panes: `MouseModes` is deliberately not persisted, so on the first attach after a daemon restart the override cannot see that the child is on the alternate screen, and those plugins have no `restoresOwnHistory` skip. Pre-existing behaviour rather than a regression from this PR, and closing it needs a design decision between persisting the bit, tracking "never scanned", or sniffing the snapshot. Filed as `techdebt/3-3-restore-replays-a-torn-alt-screen-snapshot-for-ssh.md` (round 1)
- [security] `artyom@192.168.6.12` remains recoverable from this public repo's git HISTORY. Pre-existing, unrelated to this PR, needs `git filter-repo`, already surfaced separately (round 1)
