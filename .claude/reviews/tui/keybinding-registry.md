# Code Review State: tui / keybinding-registry

Last reviewed: 2026-08-09
Rounds completed: 1

PR #138 — keybinding action registry (Stage 1). Four-agent review over
`71535ce..2927dfa`; all findings fixed in `24a938d..40a0edc` plus docs in
`e4bf51b`. Every entry below was verified against the code, not the reports.

## Resolved (fixed in code; do not re-raise)

- [code-quality/C1] `ParseChord` lowercased the base key, so `alt+M` resolved to `alt+m`'s action — on macOS with Option-as-Meta this swallowed ten shifted Meta letters that `docs/keybindings.md` documents as reaching the PTY. Single-character keys now keep case (rune count, not byte length, so `alt+Ä` ≠ `alt+ä`); named keys still fold. Pinned through `Update`, not just the parser — round 1
- [security/M-1 + code-quality/I5] F1 key column rendered config text with neither sanitizing nor truncation; a legal chord list wrapped 42 lines into a 40-line terminal, and an escape sequence reached the terminal intact. Fixed at the parser (`validateBaseKey`) so every consumer is covered, plus `truncateToWidth` for the layout half — round 1
- [code-quality/I2] Command palette `detail` column was a second unsanitized sink with the same source; closed by the parser-level fix — round 1
- [code-quality/I3] A multi-step spec (`"ctrl+b c"`) parsed, landed in `bindings`, was returned by `Display` and shown in F1 as bound — while dispatching nothing and reporting no conflict. Now reports `not yet dispatched` — round 1
- [code-quality/I4] `TestShortcutsDialog_ConflictRowSpansTheKeyColumn` asserted `full != desc + dialogKeyColWidth` — an identity. Replaced with an assertion on the rendered row — round 1
- [qa/3] The refactor hollowed out `reconnect_test.go`: it never calls `initKeymap()`, so `m.keymap` was nil, `Keys()` returned nil, and `isFreezeEscape` fell back to hardcoded `["ctrl+q","ctrl+c"]` — containing the very key those tests press. Mutating the action ID left the suite green. `TestFreezeInput_HonoursTheConfiguredQuitBinding` pins the wiring with a non-default binding — round 1
- [code-quality/I6] `TestChord_RoundTripsBubbleTea` was named and documented for a guarantee it never checked — it compared `ParseChord` against itself, never constructing a `tea.KeyPressMsg`. Renamed `TestChord_CanonicalFormIsStable`; `chord.go`'s comment repointed at the test that actually pins it — round 1
- [qa/1] `Ctrl+N` and `Alt+1..9` had zero dispatch coverage — verified by deleting each case arm and running green. `TestHandleKey_ReservedKeysDispatch` added — round 1
- [qa/2] `TestHandleKey_EveryDispatchedActionHasACaseArm` did not verify *which* switch an arm sits in; moving `project.new` across the tier seam left the suite green. It now locates the `MatchTier(keymap.TierLate, key)` boundary and asserts each tier's arms fall on the correct side — round 1
- [security/L-2] `ConflictUnknownAction` used `%s` where every sibling clause uses `%q`, and never named the offending chord. Both fixed — matters from Stage 3, when config supplies action IDs — round 1
- [security/L-3] `km.chords` was seeded for the two known tiers only; a third tier would nil-map-panic before the TUI started. Lazy-init added — round 1
- [code-quality/Q2c] `keyspecs.go` claimed "no other TUI code changes" for Stage 3, but the notes path still reads `cfg.Keybindings`. Comment now states Stage 2 as a hard prerequisite — landing Stage 3 first would break Alt+E — round 1
- [code-quality/I7] `docs/keybindings.md` was not updated for user-visible changes. Both it and `docs/configuration.md` now cover normalization rules, the conflict warnings, and an honest tmux roadmap section — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)

- [code-quality/Q2a] `Keymap.Bindings()` has no production caller — kept deliberately as the Stage 2 prefix-machine API rather than deleted and re-added (round 1)
- [code-quality/Q2b] `kbDisplay`/`kbBindings` survive as test-side oracles with no production caller — deliberate, documented in `keymatch.go`'s deprecation header, removed with the notes path in Stage 2 (round 1)
- [code-quality/suggestion] `ActionsByGroup()` rebuilds and sorts per call, including per scroll keypress — trivial at 41 actions; `sync.OnceValues` deferred (round 1)
- [security/out-of-scope] Internal test host `artyom@192.168.6.12` is scrubbed from the working tree but recoverable from this public repo's git history (~8 commits). Pre-existing, unrelated to this PR, and needs `git filter-repo` rather than a forward-only fix — surfaced to the user as a separate decision (round 1)
