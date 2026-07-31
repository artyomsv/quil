package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

func TestPluginListResponse_ReportsTheDaemonsOwnRegistry(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.DetectAvailability()

	out := pluginListResponse(reg)
	if len(out.Plugins) == 0 {
		t.Fatal("Plugins is empty — the daemon reported no registry at all")
	}
	var found bool
	for _, p := range out.Plugins {
		if p.Name == "terminal" {
			found = true
			if !p.Available {
				t.Error("terminal Available = false — the Go built-in is always available")
			}
		}
	}
	if !found {
		t.Error("the built-in terminal plugin is missing from the report")
	}
}
