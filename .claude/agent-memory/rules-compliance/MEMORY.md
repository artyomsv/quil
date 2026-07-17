# Memory Index — rules-compliance (quil)

- [Goroutine shutdown-path convention](goroutine-shutdown-convention.md) — quil's daemon/CLI goroutines use done-channels or os.Exit, not context.Context; treat as satisfying go-conventions.md's goroutine rule, not a violation.
