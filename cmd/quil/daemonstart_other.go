//go:build !windows

package main

import "errors"

// spawnLowered is unreachable off Windows: InKillOnCloseJob answers false
// there, so the decision never asks for it.
func spawnLowered(quild, quilDir string) (int, error) {
	return 0, errors.New("lowered spawn is Windows-only")
}
