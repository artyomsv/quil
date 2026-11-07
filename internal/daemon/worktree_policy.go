package daemon

import (
	"log"

	"github.com/artyomsv/quil/internal/ipc"
)

// handleWorktreePolicy stores the pushed remote-branch-delete switch.
//
// A malformed payload changes nothing: decoding it into a zero struct would
// read as "off", which is safe, but a frame nobody can read must not move a
// setting in either direction. Admin class (rights.go), so only a full-rights
// client reaches here.
func (d *Daemon) handleWorktreePolicy(msg *ipc.Message) {
	var p ipc.WorktreePolicyPayload
	if err := msg.DecodePayload(&p); err != nil {
		log.Printf("worktree policy: malformed payload: %v", err)
		return
	}
	if d.remoteBranchDelete.Swap(p.RemoteBranchDelete) != p.RemoteBranchDelete {
		log.Printf("worktree policy: remote branch delete %v", onOff(p.RemoteBranchDelete))
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
