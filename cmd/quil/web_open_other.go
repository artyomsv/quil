//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

// openBrowser opens url in the default browser. The URL carries no secret.
func openBrowser(url string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
