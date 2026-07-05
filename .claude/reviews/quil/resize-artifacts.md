# Code Review State: quil / resize-artifacts

Last reviewed: 2026-07-05
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [rules/H1] session.go gofmt drift — new appliedCols/appliedRows fields desynced the struct tabwriter block; gofmt-realigned — round 1
- [code-quality/L1] handleResizePane recorded the same-size guard before the Resize syscall; a failed resize became sticky. Now records only after success — round 1
- [code-quality/L2] overlayRight padded the content/sidebar gap before the SGR reset, bleeding a trailing background color into the gap. Reset moved before the pad — round 1
- [code-quality/L3] renderPreview bottom-anchored content shorter than the viewport (negative viewStart). Clamped to top-anchor — round 1
- [code-quality/nit] showOverlay now re-asserts SetCanvas before Resize for future wide-canvas overlays — round 1
- [code-quality/M1] escapeClaudeCWD >200-char hash branch: verified against the extracted claude binary (hashes the ORIGINAL cwd via Pke(e)/Z9u(e), not the dashified string; format `slice(0,200)-base36`). Test comment strengthened to cite the transcribed algorithm and the fail-safe (--continue) behavior — round 1
- [qa/coverage] Added: real-spawnPane guard reset, failed-resize-does-not-stick, syncPaneMeta wide_canvas propagation, restoreTabLayout + resync-in-tree (mid-migration flip) canvas paths, toggle_wrap key dispatch, cursor-past-first-wrap-segment mapping, direct locate() round-trip — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/L2] commit 695e31d subject "toggleable soft-wrap…" is a noun phrase, not imperative — not rewriting already-authored branch history for a LOW cosmetic; body is clear (round 1)
- [rules/L3] revert commit a4cc260 uses git's standard `Revert "…"` format, which isn't in the enumerated type list — git-generated, no action (round 1)
- security: clean — Semgrep 0 findings; path sanitizer, ANSI compositor, PluginMu discipline, and the TOML flag all verified sound (round 1)
