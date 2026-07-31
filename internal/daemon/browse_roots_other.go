//go:build !windows

package daemon

import "time"

// filesystemRoots reports nothing on Unix.
//
// "/" is the only root and has nothing above it, so there is no drive list to
// offer and the browser simply omits its "up" row there. Returning nil rather
// than []string{"/"} is deliberate: a root that lists itself as its own parent
// renders a row that navigates to where the user already is.
//
// The deadline is unused here and that is the point: with no drives to probe
// there is no blocking call to bound. It stays in the signature so the two
// platform variants remain interchangeable at the call site, which is what lets
// the caller hold one deadline for the whole response without a build tag of
// its own.
func filesystemRoots(time.Time) []string { return nil }
