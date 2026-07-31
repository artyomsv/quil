package daemon

import (
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
func pluginListResponse(reg *plugin.Registry) ipc.PluginListRespPayload {
	all := reg.All()
	out := ipc.PluginListRespPayload{Plugins: make([]ipc.PluginInfo, 0, len(all))}
	for _, p := range all {
		out.Plugins = append(out.Plugins, ipc.PluginInfo{Name: p.Name, Available: p.Available})
	}
	return out
}
