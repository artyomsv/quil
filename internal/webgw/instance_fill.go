package webgw

import (
	"fmt"

	"github.com/artyomsv/quil/internal/instances"
	"github.com/artyomsv/quil/internal/plugin"
)

// expandInstance is the one place a page's saved-instance id becomes
// arguments: the instance from this machine's instances.json, expanded
// through the plugin's arg_template from this machine's plugins directory —
// exactly what the TUI dialog does with its own files. Plugin definitions are
// read per call: a submit is rare, and nothing goes stale.
func (s *Server) expandInstance(pluginType, id string) (string, []string, error) {
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDirQuiet(s.cfg.PluginsDir); err != nil {
		return "", nil, fmt.Errorf("plugins: %w", err)
	}
	p := reg.Get(pluginType)
	if p == nil {
		return "", nil, fmt.Errorf("unknown plugin %q", pluginType)
	}
	store, err := instances.Load(s.cfg.InstancesPath)
	if err != nil {
		return "", nil, fmt.Errorf("instances.json: %w", err)
	}
	return instances.Expand(store, p.Command.ArgTemplate, pluginType, id)
}
