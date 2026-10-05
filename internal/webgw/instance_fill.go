package webgw

import (
	"fmt"

	"github.com/artyomsv/quil/internal/instances"
)

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
	store, err := instances.Load(s.cfg.InstancesPath)
	if err != nil {
		return "", nil, fmt.Errorf("instances.json: %w", err)
	}
	return instances.Expand(store, tmpl, pluginType, id)
}
