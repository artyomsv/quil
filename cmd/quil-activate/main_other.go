//go:build !windows

// Command quil-activate is Windows-only. This stub exists so `go build ./...`
// and `go vet ./...` cover the package on the platform CI runs, which is the
// only platform that compiles the code under the sibling //go:build windows
// tag. Without it the helper would be built for the first time by a release.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "quil-activate: desktop-toast activation is Windows-only")
	os.Exit(1)
}
