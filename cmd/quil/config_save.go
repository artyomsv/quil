package main

import "github.com/artyomsv/quil/internal/config"

// saveExitConfig writes, onto a fresh read of path, only the settings this
// TUI changed since launch (config.MergeChanged). The whole-struct save it
// replaces erased a change the browser or another TUI made meanwhile.
// [remote] needs no special case any more: the TUI never changes it, so
// MergeChanged never copies it.
func saveExitConfig(path string, launch, current config.Config) error {
	return config.Mutate(path, func(c *config.Config) { config.MergeChanged(c, launch, current) })
}
