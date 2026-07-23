# Code Review State: tui / command-palette

Last reviewed: 2026-07-23
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/M-1] Paste while the command palette is open no longer falls through to `sendClipboardToPane` — added a `dialogCommandPalette` branch in the `tea.PasteMsg` switch that folds clipboard text into the query via `sanitizePaletteQuery` (printable-only). Fixes clipboard leak into the hidden pane's PTY — round 1
- [code-quality/S-1] `renderPaletteRow` truncated labels by rune count, overflowing on wide glyphs (CJK/emoji tab names). Now cell-aware via `truncateToWidth`, and the detail column is bounded too so a long shortcut cannot overflow a narrow row — round 1
- [code-quality/S-2] Documented the greedy-subsequence tradeoff in `fuzzyScore` — round 1
- [qa/H-1] `executePaletteCommand` dispatch — added `TestPalette_DispatchOpensExpectedDialog` (all dialog-opening actions) + `TestPalette_DispatchNonDialogActions` (rename/focus/notes + cmd-only) — round 1
- [qa/H-2] Cross-tab `palActGoToPane` focus ordering now covered by `TestPalette_GoToPaneCrossTab` (two-tab model) — round 1
- [qa/M-various] Added `ctrl+n`/`ctrl+p` alias, down-clamp, space-branch, `truncateToWidth`, and wide-label render tests — round 1
- [rules/M-1] Command palette documented in `.claude/CLAUDE.md` Key Conventions — round 1
- [rules/M-2] Reworded three noun-phrase commit subjects to imperative mood — round 1
- [rules/M-3] Renamed `palette_integration_test.go` → `palette_behavior_test.go` (fast Model test, not a build-tagged integration suite) — round 1
- [rules/L-1] Renamed `TestDefault_CommandPaletteKeybinding` → `TestDefaultKeybindings_CommandPalette` to match its sibling — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)
