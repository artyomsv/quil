//go:build !windows

package opencodehook

import "syscall"

// oNoFollow makes os.OpenFile fail with ELOOP if the final path component is
// a symlink — closes the TOCTOU window the previous Lstat+Open pattern had.
const oNoFollow = syscall.O_NOFOLLOW
