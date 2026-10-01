//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isConnRefused: Windows reports WSAECONNREFUSED, which syscall.ECONNREFUSED
// does not match.
func isConnRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
