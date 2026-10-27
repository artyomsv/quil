# binswap: RestoreMissing picks the newest backup, the test expects the canonical one

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Trivial |
| Location | `internal/binswap/binswap.go:106` (`RestoreMissing`), `internal/binswap/binswap_test.go:408` (`TestRemoveBackups_KeepsBackupWithoutTarget`) |
| Found during | CI of PR #279 (sbx hooks), 2026-10-27 — the failure is unrelated to that PR |
| Date | 2026-10-27 |

## Issue

`RestoreMissing` restores the backup with the NEWEST modification time among
`<target>.old` and `<target>.old.N`. The test writes `.old` and then `.old.1`
in two consecutive calls and expects the CANONICAL `.old` to be restored. On a
filesystem with fine mtime granularity (CI's ext4/tmpfs) `.old.1` is newer and
wins, and the test fails; on a coarse one the two tie and the canonical slot
wins by iteration order. Observed: run 40544583192 failed, the next runs on
other branches passed. The test is timing-dependent, and the function's rule
("newest") and the test's rule ("canonical") disagree.

## Risks

- A flaky required check: any PR can go red on this test with no related change.
- If "canonical first" is the intended repair rule, the function restores the
  wrong copy on a fine-grained filesystem after a multi-slot swap.

## Suggested Solutions

1. Decide the rule. If canonical wins: prefer `<target>.old` when it exists,
   fall back to the newest numbered slot. If newest wins: fix the test to set
   distinct mtimes (`os.Chtimes`) and expect `.old.1`.
2. Either way, make the test deterministic with `os.Chtimes` so it does not
   depend on the filesystem's clock resolution.
