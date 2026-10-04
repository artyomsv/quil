package tui

import "github.com/artyomsv/quil/internal/instances"

// The store moved to internal/instances so the web gateway reads and writes
// the same file with the same code.
type (
	SavedInstance = instances.Saved
	InstanceStore = instances.Store
)

func LoadInstances(path string) InstanceStore              { return instances.LoadOrEmpty(path) }
func SaveInstances(path string, store InstanceStore) error { return instances.Save(path, store) }
func BuildArgs(template []string, fields map[string]string) []string {
	return instances.BuildArgs(template, fields)
}
