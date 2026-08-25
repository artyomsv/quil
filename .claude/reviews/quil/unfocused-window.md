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
- [code-quality, round 1 re-review] the round-1 rules edit ITSELF cited paneHardwareCursor as live, 47 lines after correcting the section that says it does not exist; width invariant now hangs on renderTabBar + the cell-loop renderers — round 1
- [code-quality, verified by running rg --color] SGR 58 producer list narrowed to what was actually confirmed; rg and bat named as NON-producers so a future reader checking them does not read the case as a false alarm — round 1
- [security] NaN in unfocused_dim defeated the clamp (fails both comparisons) and survived to the blend; UnfocusedDimAmount now names NaN explicitly rather than relying on View's amount > 0 — round 1
- [security] recorded WHY 38/48/58 is complete: the frame's SGR is regenerated from uv.Style, so the alphabet is what x/ansi's builder emits (exactly three multi-parameter emitters), not what the SGR standard defines — round 1

## Dismissed, added on re-review
- [code-quality/lowest] a malformed 48/58 followed by a VALID 38 in the same CSI run copies the remainder verbatim without marking the foreground explicit, so the stand-in can overwrite that 38 — unreachable from ultraviolet's emitter, which only writes well-formed runs; hardening means scanning the copied remainder for a 38, which the reviewer and I agree costs more than the case is worth (round 1)
- [security/theoretical] the T.416 six-parameter form "38;2;<colorspace>;r;g;b" would desync dimExtended (consumes 5) from a terminal consuming 6 — unreachable, since quil's own emitter only ever writes the five-parameter form and the frame is regenerated rather than passed through (round 1)
