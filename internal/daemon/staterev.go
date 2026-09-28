package daemon

import "github.com/artyomsv/quil/internal/ipc"

// handleStateReq answers with the current full state, numbered like every
// other state frame, carrying the request's ID. A conn does not need to have
// attached: an MCP bridge or a script asks for the state this way, and a TUI
// asks after a frame it could not decode.
func (d *Daemon) handleStateReq(conn *ipc.Conn, msg *ipc.Message) {
	respondTo(conn, msg.ID, ipc.MsgWorkspaceState, d.buildWorkspaceState())
}
