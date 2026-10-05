package tui

import (
	"log"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/instances"
)

// The store moved to internal/instances so the web gateway reads and writes
// the same file with the same code.
type (
	SavedInstance = instances.Saved
	InstanceStore = instances.Store
)

func LoadInstances(path string) InstanceStore { return instances.LoadOrEmpty(path) }
func BuildArgs(template []string, fields map[string]string) []string {
	return instances.BuildArgs(template, fields)
}

// instancesUnreadableFlash is what an add or delete says when instances.json
// does not parse: writing the in-memory copy over it would erase it.
const instancesUnreadableFlash = "instances.json does not parse: instance list not saved"

// mutateInstances re-reads instances.json, applies fn to the fresh copy and
// writes it back. The web gateway writes the same file, so saving the copy
// read at start would erase what a browser added and revive what it deleted.
// A file that does not parse is refused with a flash and left unchanged; the
// in-memory list then keeps its last good state.
func (m *Model) mutateInstances(fn func(InstanceStore)) bool {
	path := config.InstancesPath()
	store, err := instances.Load(path)
	if err != nil {
		log.Printf("load instances: %v", err)
		m.setFlash(instancesUnreadableFlash)
		return false
	}
	fn(store)
	if err := instances.Save(path, store); err != nil {
		log.Printf("save instances: %v", err)
		m.setFlash("instances.json not saved")
		return false
	}
	m.instanceStore = store
	return true
}

// addInstance appends inst to plugin's saved instances on disk.
func (m *Model) addInstance(plugin string, inst SavedInstance) bool {
	return m.mutateInstances(func(s InstanceStore) {
		s[plugin] = append(s[plugin], inst)
	})
}

// deleteInstance removes plugin's saved instance id on disk.
func (m *Model) deleteInstance(plugin, id string) bool {
	return m.mutateInstances(func(s InstanceStore) {
		list := s[plugin]
		for i, inst := range list {
			if inst.ID == id {
				s[plugin] = append(list[:i:i], list[i+1:]...)
				break
			}
		}
	})
}
