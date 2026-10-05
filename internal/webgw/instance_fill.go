package webgw

import (
	"errors"
	"fmt"

	"github.com/artyomsv/quil/internal/instances"
)

// errInstancesUnreadable is what the page reads when instances.json cannot be
// loaded. The cause, with its absolute path, goes to web.log only.
var errInstancesUnreadable = errors.New("saved instances could not be read")

// expandInstance is the one place a page's saved-instance id becomes
// arguments: the instance from this machine's instances.json, expanded
// through the plugin's arg_template from the catalog that /api/client lists
// (spec 5b E7) — so a plugin the dialog shows is the plugin a submit expands.
// A plugin with no form fields manages no instances, and is refused.
func (s *Server) expandInstance(pluginType, id string) (string, []string, error) {
	tmpl, ok := s.catalog.argTemplate(pluginType)
	if !ok {
		return "", nil, fmt.Errorf("plugin %q has no saved instances", pluginType)
	}
	store, err := s.loadInstances()
	if err != nil {
		s.cfg.Logf("instances.json not read: %v", err)
		return "", nil, errInstancesUnreadable
	}
	return instances.Expand(store, tmpl, pluginType, id)
}

// loadInstances reads instances.json under instMu. Holding the lock keeps
// this process's reads off the file while its own save renames over it: on
// Windows an open reader makes that rename fail.
func (s *Server) loadInstances() (instances.Store, error) {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	return instances.Load(s.cfg.InstancesPath)
}
