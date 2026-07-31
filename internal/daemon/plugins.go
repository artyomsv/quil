package daemon

import (
	"sort"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// handlePluginListReq answers "which pane plugins can actually run here".
//
// Answered inline rather than on a worker goroutine, and with no single-flight
// guard: this reads an in-memory map under the registry's own RLock. Detection
// itself already ran at daemon start and again on every plugin reload, so there
// is no filesystem call to bound. Copying the browse/discover machinery here
// would reproduce its form without its reason.
func (d *Daemon) handlePluginListReq(conn *ipc.Conn, msg *ipc.Message) {
	respondTo(conn, msg.ID, ipc.MsgPluginListResp, pluginListResponse(d.registry))
}

// pluginListResponse is the pure half.
//
// Reads through Registry.Availability rather than All(): All hands out live
// *PanePlugin pointers and drops the lock, so touching p.Available off them
// races DetectAvailability, which writes that field under the write lock. Two
// attached clients are enough — ipc runs one dispatch loop per connection.
//
// Sorted by name so the response is reproducible. Map iteration order is
// random, which makes an otherwise-identical answer differ frame to frame and
// leaves a test unable to assert on order.
func pluginListResponse(reg *plugin.Registry) ipc.PluginListRespPayload {
	avail := reg.Availability()
	out := ipc.PluginListRespPayload{Plugins: make([]ipc.PluginInfo, 0, len(avail))}
	for name, ok := range avail {
		out.Plugins = append(out.Plugins, ipc.PluginInfo{Name: name, Available: ok})
	}
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].Name < out.Plugins[j].Name })
	return out
}
