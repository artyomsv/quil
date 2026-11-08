//go:build windows

package gitworktree

// markOpenFlags: Windows has no FIFO special file to block on and no
// O_NOFOLLOW; the Lstat-then-SameFile check refuses a reparse point.
const markOpenFlags = 0
