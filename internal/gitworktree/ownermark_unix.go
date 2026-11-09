//go:build !windows

package gitworktree

import "syscall"

// markOpenFlags opens the owner mark without following a symlink and without
// blocking on a FIFO: the file is checked after it is opened.
const markOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
