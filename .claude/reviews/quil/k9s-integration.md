# Code Review State: quil / k9s-integration

Last reviewed: 2026-06-15
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [rules/H-1] deprecationWarned moved from package global to a Registry field — round 1
- [rules/H-2] TestDefaultPluginTOMLFiles now checks all 5 defaults (added lazygit, k9s) — round 1
- [rules/M-4 + code-quality/S-4] site k9s spawnExample schema_version corrected 1 → 2 — round 1
- [rules/M-5] lazygit.toml (→2) and stripe.toml (→1) schema_version bumped for the new homepage field — round 1
- [rules/M-7] CLAUDE.md documents internal/kubediscover and discover="kube" — round 1
- [rules/L-6] kubediscover tests consolidated table-driven — round 1
- [code-quality/S-1] symlinked kubeconfig now resolved (target read) instead of rejected — round 1
- [code-quality/S-2] j/k navigation made consistent across all setup-dialog fields — round 1
- [code-quality/S-3] kube namespace suffix no longer nests an ANSI reset that truncated the selected-row highlight — round 1
- [security/L-1] context name/namespace stripped of control characters at the kubediscover source (feeds both render and --context) — round 1
- [qa/G-1] handleSetupKubeKey up/down clamp + enter-dispatch tests added — round 1
- [qa/G-2] maxKubeContexts cap test added — round 1
- [qa/G-3] maxContentLineWidth direct unit test added — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- (none)
