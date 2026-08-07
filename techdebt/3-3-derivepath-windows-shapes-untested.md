# `gitworktree.DerivePath` is untested against Windows path shapes

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Medium |
| Location | `internal/gitworktree/validate.go:100-120` (`DerivePath`), `internal/gitworktree/validate_test.go` |
| Found during | QA of PR #134 (worktree creation), re-confirmed by the security review of PR #135 |
| Date | 2026-08-07 |

## Issue

`DerivePath` decides where a new worktree is created:

```go
func DerivePath(repoRoot, branch string) string {
    parent := filepath.Dir(repoRoot)
    name := filepath.Base(repoRoot) + worktreesSuffix
    return filepath.Join(parent, name, strings.ReplaceAll(branch, "/", "-"))
}
```

Every operation here — `Dir`, `Base`, `Join` — is **GOOS-dependent**. On Linux
`\` is an ordinary filename character; on Windows it is a separator. The daemon
runs on both.

The tests build every fixture with the same `filepath` helpers
(`validate_test.go`), so on the Linux CI container they exercise `/`-separated
paths only. A real Windows daemon's `repoRoot` is `C:\Users\x\quil`, which on
Linux `filepath.Base` returns **whole** — the tests cannot observe that, and no
`_windows.go` variant pins the behaviour.

The security review measured two related facts natively on Windows that the
suite likewise cannot see:

- `filepath.IsAbs("C:repo")` is **false** — drive-relative paths are rejected
  by the absoluteness guard, which is the desired outcome but is unverified by
  CI.
- `filepath.IsAbs(\\server\share\repo)` is **true** — UNC roots are accepted
  and derive a sibling under the share.

Nothing is known to be broken. The gap is that the correctness of the feature's
central path calculation rests on reasoning and one manual measurement rather
than on a test that runs.

## Risks

`DerivePath` is where the no-nested-worktree guarantee lives. PR #134 shipped a
bug in exactly this area — the caller passed the browsed directory instead of
the repository root, which put a full second checkout **inside** the repository
— and the consequence there was that a `git clean -xfd` in the main checkout
would delete another pane's uncommitted work. A separator-handling error on
Windows lands in the same blast radius, and the Linux suite would stay green
through it.

Secondary: `windowsReserved` / `isReservedName` (added by PR #135) is also
Linux-only tested. It is pure string work so the risk is lower, but it is
motivated entirely by Windows filesystem behaviour.

## Suggested Solutions

1. **Add a Windows CI leg** for at least `./internal/gitworktree/` and
   `./internal/gitinfo/`. Most faithful, and it also covers the `_windows.go`
   files elsewhere that CI never compiles — the project has been bitten by
   "not directly testable in CI, the Linux image never compiles this file"
   before (see the `sweepRoots` note in `.claude/rules/remote-dialogs.md`).
2. **Extract the separator-independent half** — the branch flattening and the
   `<repo>-worktrees` suffix — into a pure string function tested without
   `path/filepath`, leaving only the `Dir`/`Base`/`Join` composition
   platform-dependent. Cheaper, and narrows what remains unverified.
3. **Cross-compile a probe and run it on a Windows host**, as the security
   review did (`GOOS=windows go test -c`, run the `.exe` natively). Already
   recorded as the project's technique for this in agent memory; makes the
   check repeatable but still manual.

Option 2 is the smallest useful step; option 1 is what actually closes it.
