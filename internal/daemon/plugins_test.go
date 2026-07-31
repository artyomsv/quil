package daemon

import (
	"sync"
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

// TestPluginListResponse_RacesReload pins the lock discipline that
// Registry.Availability exists for.
//
// The first version read p.Available off the pointers Registry.All() hands out
// AFTER it releases the RLock, which races DetectAvailability's writes under the
// write lock. It is reachable in production with two attached clients — ipc runs
// one dispatch goroutine per connection, so a plugin reload on one can overlap a
// list request on another — but no ordinary test drives that interleaving, so
// -race passed over it.
//
// Fails under -race if pluginListResponse goes back to reading through All().
func TestPluginListResponse_RacesReload(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.DetectAvailability()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				reg.DetectAvailability()
			}
		}
	}()

	// Read on THIS goroutine, then stop the writer and join. Waiting first
	// would deadlock: the writer only returns once stop is closed.
	for i := 0; i < 200; i++ {
		if out := pluginListResponse(reg); len(out.Plugins) == 0 {
			t.Fatal("empty plugin list from a registry that has the terminal built-in")
		}
	}
	close(stop)
	wg.Wait()
}
