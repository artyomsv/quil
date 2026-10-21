//go:build windows

package main

import "errors"

// execSelf is unavailable on Windows: there is no exec that replaces a
// process image, so respawnSelf keeps the child-plus-wrapper relaunch there.
func execSelf(exe string, args, env []string) error {
	return errors.ErrUnsupported
}
