package daemon

import (
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/google/uuid"
)

// agentIdleSettle is how long a pane must stay idle after its work ledger
// falls before the daemon calls the turn finished.
//
// The falling edge alone is not enough: when a teammate reports back, Claude
// resumes on its own — a user-ROLE transcript entry with no UserPromptSubmit —
// and the next edge is a PreToolUse heartbeat up to workHeartbeatInterval
// later. Two seconds catches the immediate resume (the common case, measured
// as Stop → PreToolUse within ~1 s) without holding a genuinely finished task
// for long. A package var so tests can shorten it.
var agentIdleSettle = 2 * time.Second

// applyWorkEvent feeds one event into the pane's work ledger and arms or
// disarms the settle window. Called from emitEvent BEFORE the mute and
// work-state-only bypasses, so the daemon's ledger sees exactly the edges the
// TUI's does — a muted pane's Stop and a PreToolUse heartbeat included.
func (d *Daemon) applyWorkEvent(pane *Pane, e PaneEvent) hookevents.Transition {
	pane.workMu.Lock()
	defer pane.workMu.Unlock()
	tr := pane.Work.Apply(e.Type, e.Data, e.Timestamp)
	if tr.Now == hookevents.WorkWorking && pane.idleTimer != nil {
		// Working again inside the settle window: the earlier fall was a
		// pause between turns, not a completion.
		pane.idleTimer.Stop()
		pane.idleTimer = nil
	}
	if tr.FellIdle {
		if pane.idleTimer != nil {
			pane.idleTimer.Stop()
		}
		id := pane.ID
		pane.idleTimer = time.AfterFunc(agentIdleSettle, func() { d.settleIdle(id) })
	}
	if tr.Aborted {
		// A crash is not a completion, but nothing will ever fire for this
		// pane again: run the subscribers so a deferred notify-back or a task
		// waiting on this pane is not stranded.
		//
		// They are told the pane ABORTED, and that flag is what makes the
		// abort beat the completion. emitEvent runs this before taskObserve
		// marks the pane's tasks failed, and the subscribers run on their own
		// goroutine — so a completion callback that ignored the flag could
		// report a crashed pane as done, with scheduling deciding which
		// answer the requester got.
		if pane.idleTimer != nil {
			pane.idleTimer.Stop()
			pane.idleTimer = nil
		}
		subs := pane.idleSubs
		pane.idleSubs = nil
		go func() {
			for _, fn := range subs {
				fn(true)
			}
		}()
	}
	return tr
}

// settleIdle runs when the settle window elapses. It re-checks the ledger —
// the timer may have raced a start edge — then emits agent_idle and runs the
// idle subscribers once.
func (d *Daemon) settleIdle(paneID string) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	pane.workMu.Lock()
	pane.idleTimer = nil
	if pane.Work.State() != hookevents.WorkIdle {
		pane.workMu.Unlock()
		return
	}
	// An abort leaves the ledger reporting idle, so this window can close on a
	// pane whose process has since exited (the abort stops the timer, but one
	// already firing is past that). The subscribers still RUN — dropping them
	// strands whoever is waiting — but they are told the truth, and no
	// "Turn finished" is announced for a pane that died.
	aborted := pane.Work.Aborted()
	subs := pane.idleSubs
	pane.idleSubs = nil
	pane.workMu.Unlock()

	if aborted {
		for _, fn := range subs {
			fn(true)
		}
		return
	}

	pane.PluginMu.Lock()
	name := pane.Name
	pane.PluginMu.Unlock()
	d.emitEvent(withExcerpt(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     pane.CurrentTabID(),
		PaneName:  name,
		Type:      "agent_idle",
		Title:     "Turn finished",
		Severity:  "info",
		Timestamp: time.Now(),
		Data:      map[string]string{},
	}, paneOutputExcerpt(pane, 5)))
	for _, fn := range subs {
		fn(false)
	}
}

// idleSub is what onPaneIdle registers. aborted=true means the pane's process
// exited: final, but the opposite of a completion.
type idleSub func(aborted bool)

// onPaneIdle registers fn to run once the pane next settles idle. If the pane
// is CONFIRMED idle — the ledger says WorkIdle and no settle window is open —
// fn runs immediately on the caller's goroutine, because a subscriber must not
// wait for an edge that already happened. Returns false when the pane does not
// exist.
//
// WorkUnknown is NOT idle, and treating it as idle was a real defect. It means
// no classified edge has been seen for this pane at all — an AI pane whose
// hooks never loaded reports it for its whole life while working exactly as
// hard as any other. Running immediately there types a completion notice into
// a live turn, which is the input corruption this guard exists to prevent. A
// subscriber on such a pane waits for a real idle edge instead, and is simply
// never delivered if none ever comes: the notify-back needs hook integration,
// where the task_done event does not.
//
// An ABORTED pane is not idle either, and the abort flag in applyWorkEvent
// cannot cover this on its own: it reaches the subscribers ALREADY in the
// list, and a subscriber can arrive afterwards. emitEvent applies the event to
// the ledger and observes it for tasks in two separate steps, so two
// goroutines emitting concurrently interleave as apply(Stop), apply(exit),
// observe(Stop) — and by the time that Stop registers its completion callback,
// the abort has drained the list, cleared the settle timer, and left the
// ledger reporting WorkIdle. The subscriber then ran immediately, as a
// completion, and reported a crashed pane as done. Ledger.Aborted() is the
// pane's standing answer rather than one event's, so a late arrival gets the
// same truth an early one did. It runs NOW rather than waiting, because
// nothing will ever fire for a dead pane again.
func (d *Daemon) onPaneIdle(paneID string, fn idleSub) bool {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return false
	}
	pane.workMu.Lock()
	state := pane.Work.State()
	aborted := pane.Work.Aborted()
	settling := pane.idleTimer != nil
	if aborted {
		pane.workMu.Unlock()
		fn(true)
		return true
	}
	if state == hookevents.WorkIdle && !settling {
		pane.workMu.Unlock()
		fn(false)
		return true
	}
	pane.idleSubs = append(pane.idleSubs, fn)
	pane.workMu.Unlock()
	return true
}

// paneWorkState reads the ledger for the wire.
func paneWorkState(pane *Pane) (state, reason string, lastIdle int64) {
	pane.workMu.Lock()
	defer pane.workMu.Unlock()
	state = string(pane.Work.State())
	reason = pane.Work.BlockedReason()
	if t := pane.Work.LastIdleAt(); !t.IsZero() {
		lastIdle = t.UnixMilli()
	}
	return state, reason, lastIdle
}
