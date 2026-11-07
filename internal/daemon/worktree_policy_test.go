package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// The pushed value is what every remote delete reads; a malformed frame moves
// nothing.
//
// Fails if the handler stores the decoded value on a decode error, or never
// stores it.
func TestHandleWorktreePolicy_SetsRuntimeFlag(t *testing.T) {
	d := &Daemon{}

	on, err := ipc.NewMessage(ipc.MsgWorktreePolicy, ipc.WorktreePolicyPayload{RemoteBranchDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	d.handleWorktreePolicy(on)
	if !d.remoteBranchDelete.Load() {
		t.Fatal("policy push did not switch remote branch delete on")
	}

	d.handleWorktreePolicy(&ipc.Message{Type: ipc.MsgWorktreePolicy, Payload: []byte(`"not an object"`)})
	if !d.remoteBranchDelete.Load() {
		t.Error("a malformed frame switched the policy off")
	}
}
