package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// stateMark is the newest state frame applied from one destination.
type stateMark struct {
	runID string
	rev   uint64
}

// stateDecodeFailedMsg reports a workspace_state frame that could not be
// decoded; it was dropped in the listen loop.
type stateDecodeFailedMsg struct{ Dest string }

// stateReqTimeoutMsg ends one state_req's single-flight window. Gen stops a
// timer from an earlier request clearing a newer one.
type stateReqTimeoutMsg struct {
	Dest string
	Gen  uint64
}

// stateReqTimeout bounds how long a state_req blocks the next one. A var so
// tests can shorten it.
var stateReqTimeout = 8 * time.Second

// acceptStateRev decides whether a state frame is applied. It must run
// before anything else in the WorkspaceStateMsg arm, so a dropped frame has
// no side effect.
func (m *Model) acceptStateRev(msg WorkspaceStateMsg) bool {
	if msg.Rev == 0 {
		return true // an older daemon numbers nothing
	}
	if m.stateSeen == nil {
		m.stateSeen = map[string]stateMark{}
	}
	mark := m.stateSeen[msg.Dest]
	if mark.runID == msg.RunID && msg.Rev <= mark.rev {
		log.Printf("workspace_state from %q: rev %d <= %d, dropped as stale", msg.Dest, msg.Rev, mark.rev)
		return false
	}
	m.stateSeen[msg.Dest] = stateMark{runID: msg.RunID, rev: msg.Rev}
	// Any applied frame is current state, so an outstanding state_req's
	// answer is no longer needed.
	delete(m.stateReqGen, msg.Dest)
	return true
}

// requestStateFor asks one destination for a full state after a frame from
// it could not be decoded. Only a daemon that has numbered a frame answers
// state_req; single-flight per destination.
func (m *Model) requestStateFor(dest string) tea.Cmd {
	if m.stateSeen[dest].rev == 0 {
		return nil
	}
	if _, pending := m.stateReqGen[dest]; pending {
		return nil
	}
	if m.stateReqGen == nil {
		m.stateReqGen = map[string]uint64{}
	}
	m.stateGenSeq++
	gen := m.stateGenSeq
	m.stateReqGen[dest] = gen
	send := func() tea.Msg {
		req, err := ipc.NewMessage(ipc.MsgStateReq, struct{}{})
		if err != nil {
			log.Printf("state_req: encode: %v", err)
			return nil
		}
		req.ID = "state-" + dest + "-" + time.Now().Format("150405.000000")
		m.sendForDest(dest, req)
		return nil
	}
	timeout := tea.Tick(stateReqTimeout, func(time.Time) tea.Msg {
		return stateReqTimeoutMsg{Dest: dest, Gen: gen}
	})
	return tea.Batch(send, timeout)
}

func (m *Model) endStateReq(msg stateReqTimeoutMsg) {
	if m.stateReqGen[msg.Dest] == msg.Gen {
		delete(m.stateReqGen, msg.Dest)
	}
}

// forgetStateMark drops what is known about a destination's state numbering;
// a reattach may land on a restarted daemon.
func (m *Model) forgetStateMark(dest string) {
	delete(m.stateSeen, dest)
	delete(m.stateReqGen, dest)
}
