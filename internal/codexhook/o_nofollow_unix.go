//go:build !windows

package codexhook

import "syscall"

// oNoFollow makes os.OpenFile refuse a symlink atomically (no Lstat+Open
// TOCTOU window). Mirrors internal/opencodehook.
const oNoFollow = syscall.O_NOFOLLOW
