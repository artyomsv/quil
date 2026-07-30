//go:build !windows

package daemon

// filesystemRoots reports nothing on Unix.
//
// "/" is the only root and has nothing above it, so there is no drive list to
// offer and the browser simply omits its "up" row there. Returning nil rather
// than []string{"/"} is deliberate: a root that lists itself as its own parent
// renders a row that navigates to where the user already is.
func filesystemRoots() []string { return nil }
