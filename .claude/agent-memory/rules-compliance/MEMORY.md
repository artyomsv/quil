# Memory Index — rules-compliance (quil)

- [Goroutine shutdown-path convention](goroutine-shutdown-convention.md) — quil's daemon/CLI goroutines use done-channels or os.Exit, not context.Context; treat as satisfying go-conventions.md's goroutine rule, not a violation.
- [Branch naming convention](quil-branch-naming-convention.md) — quil uses "feature/" not "feat/" for branch prefixes (CONTRIBUTING.md + repo history); flag "feat/*" branches as drift.
- [CLAUDE.md staleness: scope vs falsehood](claude-md-staleness-scope-vs-falsehood.md) — before calling a CLAUDE.md line "now false", verify the OLD path it describes is actually broken; a new path adding an uncovered case is a scope gap, not a falsehood, and needs an additive edit not a rewrite.
