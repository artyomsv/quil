- **`TestRemoveBackups_KeepsBackupWithoutTarget` no longer depends on the filesystem's
  clock resolution.** `RestoreMissing` restores the NEWEST backup; the test wrote `.old`
  and `.old.1` back to back and expected `.old`, which only held when the two mtimes
  tied. It now dates `.old.1` into the past, so the expectation follows the rule on
  every filesystem — the check failed the v1.97.0 release job once for nothing.
