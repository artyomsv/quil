# Code Review State: quil / unfocused-window

Last reviewed: 2026-08-25
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [qa/MEDIUM + code-quality] SGR 58 sub-parameters reinterpreted as top-level SGR codes; 38/48/58 now all consumed — round 1
- [code-quality] the 58 failure inverts when the index collides with 30-37/40-47/90-97/100-107 (suppressed stand-in, full-brightness text); regression row uses 58;5;31 — round 1
- [security/LOW CWE-20] sgrParams now checks the 0x20-0x3F CSI-body invariant instead of assuming it — round 1
- [security/LOW CWE-770 + code-quality] per-frame memo cache capped at 512 entries — round 1
- [self, applying the colon-form rule] malformed extended run leading with 38 now counts as an explicit foreground so a miss cannot become corruption — round 1
- [code-quality] docs/configuration.md unfocused_dim row orphaned by a blank line, rendered as literal pipes — round 1
- [code-quality] palette colours resolve against xterm defaults (hue shift on blur); documented as a known limitation in docs + rules — round 1
- [code-quality] SGR 58 producer list was wrong (bat / ripgrep --color do not emit it); corrected to undercurl-capable clients — round 1
- [rules-compliance] frame-final pass + its invariants documented in .claude/rules/tui-rendering.md, including that Blur/Focus/colour messages must never be skipRender-inert — round 1
- [code-quality] Model.dimPalette method shadowed the dimPalette type; renamed to dimInputs — round 1
- [code-quality] strconv.Atoi ran before the empty-field check — round 1
- [code-quality, out of scope but verified] tui-rendering.md "Pane cursor model" documented paneHardwareCursor()/tea.View.Cursor, both of which no longer exist — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/LOW] b.Grow(len+len/8) preallocation constant is a guess and a text-heavy frame can exceed it — it is a hint to the allocator, not a bound; being wrong costs one growth, and there is no better estimate available before the walk (round 1)
- [security/advisory] consume extended runs via ansi.ReadStyleColor so the rewriter's grammar cannot drift from the emitter's — structurally nicer than the enumerated 38/48/58 fix, but security assessed the enumerated fix plus a regression test as proportionate and the drift risk requires x/ansi adding a fourth multi-parameter emitter (round 1)
