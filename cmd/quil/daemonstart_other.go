//go:build !windows

package main

import "errors"

// spawnLowered is unreachable off Windows: JobState answers "not in a job"
// there, so the decision never asks for it.
func spawnLowered(quild, quilDir string) (int, error) {
	return 0, errors.New("lowered spawn is Windows-only")
}

// spawnLoweredInPlace is unreachable off Windows for the same reason.
func spawnLoweredInPlace(quild, quilDir string) (int, error) {
	return 0, errors.New("lowered spawn is Windows-only")
}
