# Memory Index — rules-compliance (quil)

- [Goroutine shutdown-path convention](goroutine-shutdown-convention.md) — quil's daemon/CLI goroutines use done-channels or os.Exit, not context.Context; treat as satisfying go-conventions.md's goroutine rule, not a violation.
- [Branch naming convention](quil-branch-naming-convention.md) — quil uses "feature/" not "feat/" for branch prefixes (CONTRIBUTING.md + repo history); flag "feat/*" branches as drift.
- [CLAUDE.md staleness: scope vs falsehood](claude-md-staleness-scope-vs-falsehood.md) — before calling a CLAUDE.md line "now false", verify the OLD path it describes is actually broken; a new path adding an uncovered case is a scope gap, not a falsehood, and needs an additive edit not a rewrite.
- [quil CHANGELOG.md is CI-gated, not auto-generated](quil-changelog-ci-gate.md) — any PR touching non-test cmd/ or internal/ .go files must also touch CHANGELOG.md or ci.yml's changelog job fails; ignore generic claims that it's fully auto-generated.
- [quil techdebt convention confirmed](quil-techdebt-convention.md) — no /techdebt-add command, no techdebt/README.md → flat file convention under techdebt/ applies (one pty/ subfolder precedent).
- [gofmt/CRLF check methodology](gofmt-crlf-methodology.md) — worktree is CRLF so raw `gofmt -l` flags everything; strip \r, diff parent vs. new, only flag NEW drift. model.go's PaneInfo/Model structs are the recurring drift site.
- [notify-feature-blocking-com-calls.md](notify-feature-blocking-com-calls.md) — internal/notify had unbounded COM calls on the Update goroutine; RESOLVED via bounded enqueue + SyncNotifier
