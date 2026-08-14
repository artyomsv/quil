# Code Review State: tui / progress-indicator

Last reviewed: 2026-08-14
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [rules/HIGH] CHANGELOG.md entry added under `## [Unreleased]` — `internal/tui/*.go` (non-test) survives the CI changelog denylist, so the gate requires it — round 1
- [code-quality/1] `workingGlyph` is now the ONE index→glyph definition for the work spinner; `model.go`'s tabLabel and `pane.go`'s buildTopBorder route through it instead of hand-rolling `spinnerFrames[f%len(f)]` — round 1
- [code-quality/1b] `TestBuildTopBorder_WorkingFrameMatchesTheSidebar` added — every prior border test passed frame 0, the single value where a drifted private copy still agrees — round 1
- [code-quality/2] `⚠` → `▲` in README.md (×2), docs/keybindings.md (×2), docs/roadmap.md, site/src/data/features.ts (×2). The code replaced U+26A0 precisely because a font may answer it with a wide colour emoji face, so the docs were advertising the rendering bug that was fixed — round 1
- [code-quality/4] paneRow doc parenthetical rewrapped; the 93-column line reverted to the historical `▲/◐/✓` wording it should have kept (four sibling historical references were left alone — changing one of five was the wrong number) — round 1
- [code-quality/5] `/*workFrame*/` inline comments at the two `projectRow` call sites that pass a varied frame, matching border_test.go's existing idiom — round 1
- [security/LOW-1] `workingGlyph` guards the modulo (`((f%n)+n)%n`). Unreachable today, but centralising made it one free line in a single site, and a panic on a render path takes down the whole multiplexer — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [code-quality/3] The working indicator keeps spinning for an OFFLINE destination, asserting motion about an unreachable machine. Not fixed: `projectRow` has `offline` in hand, but the pane rows, tab label and pane border do not — a lone frozen badge would put two answers about one pane on one screen, which is the exact defect this change removes. The link glyph (⚡/⟳) on the same row and the reconnect banner are the honest signal, and the previous static `◐` was equally stale, just stale-and-neutral. Decision recorded in a comment at the badge site; revisit as ONE change across all four renderers or not at all (round 1)
- [qa/optional] A dedicated assertion that a BACKGROUND project's badge animates. The mechanism has no per-project branching (`sidebarRows` passes the same `m.workSpinnerFrame` to every project row) and cross-project work-state mirroring is already covered by `TestPaneEventFromBackgroundProjectUpdatesState`; QA agreed it would not earn its keep (round 1)
- [code-quality/pre-existing] `internal/tui/pane.go`'s `spawnErrorStyle`/`restoreDoneStyle` var block is gofmt-misaligned. Verified byte-identical to `git show HEAD:internal/tui/pane.go` — pre-existing, and fixing it means a whole-file CRLF rewrite that would bury this change's diff (round 1)

## Notes
- Semgrep (`p/owasp-top-ten` + `p/golang` + `p/typescript`) ran clean with coverage verified via `paths.scanned` rather than trusting a zero-finding exit — inside a git worktree Semgrep's git-based target discovery finds zero files and still exits 0.
- The restore spinner (`pane.go`'s `p.spinnerFrame`) deliberately does NOT share `workingGlyph`: different counter, different lifecycle, describing a pane coming back rather than an agent working.
