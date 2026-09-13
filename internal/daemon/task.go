package daemon

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
)

// A task is one pane asking another for work. The daemon delivers the prompt,
// then follows the TARGET pane's work ledger to a terminal state and tells
// the REQUESTER — as a queued task_done event any MCP client can watch, and,
// when asked, as a typed line into the requester's own prompt.
//
// The registry is runtime-only and bounded. A task is bookkeeping about two
// live processes; a daemon restart respawns both and nothing could resume a
// turn that was mid-flight anyway.

type taskState string

const (
	taskSent    taskState = "sent"    // prompt queued to the target's stdin
	taskWorking taskState = "working" // target's ledger reported a start edge
	taskDone    taskState = "done"    // target settled idle
	taskFailed  taskState = "failed"  // target's process exited
	taskTimeout taskState = "timeout"
)

func (s taskState) terminal() bool {
	return s == taskDone || s == taskFailed || s == taskTimeout
}

const (
	maxTasks          = 200
	taskPromptPreview = 200
	taskResultLines   = 30
	// taskNoticeMaxLine bounds the one line typed back into the requester.
	taskNoticeMaxLine = 160
	// pasteSettle is the gap between the bracketed paste and the Enter that
	// submits it. Claude Code and codex both accept a paste and then a
	// separate CR; sending the CR inside the same write has the newline land
	// in the input buffer as text on some versions.
	//
	// CODEX is what makes this number large. Under ConPTY it does not receive
	// the bracketed-paste markers as a paste EVENT — it sees a fast run of
	// characters — so it falls back to its paste-burst heuristic
	// (codex-rs/tui/src/bottom_pane/paste_burst.rs, read at v0.154.0):
	// PASTE_ENTER_SUPPRESS_WINDOW is 120 ms and is REFRESHED on every
	// buffered character, so it ends 120 ms after the last one codex
	// processed, and PASTE_BURST_ACTIVE_IDLE_TIMEOUT is 60 ms on Windows
	// before the buffer reaches the composer at all. An Enter inside that
	// window is appended to the prompt as a newline AND extends the window by
	// another 120 ms, so it is swallowed in silence: the prompt sits in the
	// composer looking delivered and nothing ever runs. At 100 ms that was
	// the common case — measured 2026-09-13, a delegated task to a codex pane
	// never started, while the same delivery to a claude pane in the same tab
	// started in 0.6 s. Only a real bracketed-paste event clears the window
	// early (clear_after_explicit_paste), which is exactly what does not
	// happen here.
	//
	// 400 ms is 60 + 120 with margin for the lag between our write and codex
	// reading it. It is a CEILING on the gap, not a floor: the CR is timed
	// from the enqueue, and both halves cross the same ordered per-pane
	// writer, so a child that is slow to drain its stdin narrows the gap it
	// actually sees. Claude Code accepts any gap, so one value serves both.
	pasteSettle = 400 * time.Millisecond
)

type task struct {
	id       string
	from, to string
	toName   string
	prompt   string
	notify   bool
	terminal bool // target is a plain terminal: done on command_complete
	created  time.Time
	started  time.Time
	ended    time.Time
	state    taskState
	result   string
	errText  string
	notified bool
	done     chan struct{}
	timer    *time.Timer
}

type taskRegistry struct {
	mu    sync.Mutex
	byID  map[string]*task
	order []string
	max   int
}

func newTaskRegistry(max int) *taskRegistry {
	if max <= 0 {
		max = maxTasks
	}
	return &taskRegistry{byID: make(map[string]*task), max: max}
}

// addLive stores t unless a live task already targets the same pane, whose id
// it then returns instead — storing nothing. Storing evicts the oldest
// TERMINAL task past the cap; a live task is never evicted, because a waiter
// is blocked on its channel.
//
// One live task per target, because completion is read off the TARGET's work
// ledger and that ledger says nothing about WHICH prompt finished: with two
// tasks queued to one pane, the first settled idle would mark both done,
// including the one whose prompt the agent has not looked at yet. The scan and
// the insert are one critical section, or two clients delegating to the same
// pane both pass a check made before either insert.
func (r *taskRegistry) addLive(t *task) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		if old := r.byID[id]; old != nil && old.to == t.to && !old.state.terminal() {
			return old.id
		}
	}
	r.addLocked(t)
	return ""
}

func (r *taskRegistry) addLocked(t *task) {
	r.byID[t.id] = t
	r.order = append(r.order, t.id)
	for len(r.order) > r.max {
		evicted := false
		for i, id := range r.order {
			if old := r.byID[id]; old != nil && old.state.terminal() {
				delete(r.byID, id)
				r.order = append(r.order[:i], r.order[i+1:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			return
		}
	}
}

func (r *taskRegistry) get(id string) *task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// live reports whether t is still open. Every read of t.state belongs under
// the lock that writes it: taskObserve runs on whichever goroutine emitted the
// event, while the timeout callback and finishTask write from theirs.
func (r *taskRegistry) live(t *task) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !t.state.terminal()
}

// advance moves t from one state to another and reports whether it applied.
// Compare-and-set under the lock, never a check followed by a write: a timeout
// firing between the two would have its terminal state overwritten with
// `working`, after which the next completion closes t.done a SECOND time and
// panics the daemon.
func (r *taskRegistry) advance(t *task, from, to taskState, started time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.state != from {
		return false
	}
	t.state = to
	if to == taskWorking {
		t.started = started
	}
	return true
}

// arm publishes t's timeout timer, or declines to start one when the task has
// already ended.
//
// Under the lock like every other field of t: the prompt is queued BEFORE this
// runs, so a shell that answers at once — command_complete, or a process exit —
// has finishTask reading and stopping t.timer on another goroutine while this
// assignment lands. Unsynchronised, that is a data race, and the completion
// then stops a nil pointer while the timer it could not see stays scheduled.
func (r *taskRegistry) arm(t *task, after time.Duration, fire func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.state.terminal() {
		return
	}
	t.timer = time.AfterFunc(after, fire)
}

// liveTasksTo returns every open task aimed at paneID.
func (r *taskRegistry) liveTasksTo(paneID string) []*task {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*task
	for _, id := range r.order {
		if t := r.byID[id]; t != nil && t.to == paneID && !t.state.terminal() {
			out = append(out, t)
		}
	}
	return out
}

func (r *taskRegistry) all() []*task {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*task, 0, len(r.order))
	for _, id := range r.order {
		if t := r.byID[id]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

// info snapshots t for the wire. Caller holds the registry lock or accepts a
// racy read of fields the state machine writes under it.
func (r *taskRegistry) info(t *task) ipc.TaskInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return t.infoLocked()
}

func (t *task) infoLocked() ipc.TaskInfo {
	ms := func(tm time.Time) int64 {
		if tm.IsZero() {
			return 0
		}
		return tm.UnixMilli()
	}
	return ipc.TaskInfo{
		ID: t.id, FromPane: t.from, ToPane: t.to, ToPaneName: t.toName,
		State: string(t.state), Prompt: t.prompt, Result: t.result, Error: t.errText,
		CreatedAt: ms(t.created), StartedAt: ms(t.started), EndedAt: ms(t.ended),
		Notified: t.notified,
	}
}

func (d *Daemon) tasksRegistry() *taskRegistry {
	d.tasksOnce.Do(func() { d.tasks = newTaskRegistry(maxTasks) })
	return d.tasks
}

// isAgentPane reports whether the pane runs an AI plugin (category "ai"),
// which is what decides how a prompt is delivered and which edge means done.
func (d *Daemon) isAgentPane(pane *Pane) bool {
	pane.PluginMu.Lock()
	typ := pane.Type
	pane.PluginMu.Unlock()
	base, _ := splitSandboxType(typ)
	if p := d.registry.Get(base); p != nil {
		return p.Category == "ai"
	}
	return false
}

// deliverPrompt hands text to a pane's stdin the way a human would: an AI
// pane gets a bracketed paste (so embedded newlines stay inside the prompt)
// and, after a short settle, a CR; a terminal gets the text and a CR.
//
// CR, never LF: Enter on a keyboard is CR, and that is what every shell's
// line editor accepts. A Unix tty in cooked mode maps CR to NL (ICRNL), so
// CR works there too — but LF does NOT execute in PowerShell under ConPTY
// (measured 2026-09-10: the line is echoed and sits at the prompt), which is
// how a delegated `echo` printed its own text and never ran.
// Delivery means QUEUED to the pane's writer, as with every other input.
func (d *Daemon) deliverPrompt(pane *Pane, text string, agent bool) bool {
	if !agent {
		return pane.EnqueueInput([]byte(text + "\r"))
	}
	// No embedded paste delimiter may turn subsequent text into keystrokes.
	text = strings.NewReplacer("\x1b[201~", "", "\u009b201~", "", string([]byte{0x9b})+"201~", "").Replace(text)
	if !pane.EnqueueInput([]byte("\x1b[200~" + text + "\x1b[201~")) {
		return false
	}
	id := pane.ID
	time.AfterFunc(pasteSettle, func() {
		if p := d.session.Pane(id); p != nil {
			p.EnqueueInput([]byte("\r"))
		}
	})
	return true
}

func (d *Daemon) delegateTask(req ipc.DelegateTaskReqPayload) ipc.DelegateTaskRespPayload {
	refuse := func(format string, args ...any) ipc.DelegateTaskRespPayload {
		return ipc.DelegateTaskRespPayload{Error: fmt.Sprintf(format, args...)}
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return refuse("prompt is empty")
	}
	if req.ToPane == req.FromPane && req.ToPane != "" {
		return refuse("a pane cannot delegate to itself")
	}
	pane, why := d.paneInputTarget(req.ToPane)
	if pane == nil {
		return refuse("%s", why)
	}
	agent := d.isAgentPane(pane)
	pane.PluginMu.Lock()
	toName := pane.Name
	pane.PluginMu.Unlock()

	id := "task-" + uuid.New().String()[:8]
	t := &task{
		id:       id,
		from:     req.FromPane,
		to:       req.ToPane,
		toName:   toName,
		prompt:   truncateField(req.Prompt, taskPromptPreview),
		notify:   req.Notify && req.FromPane != "",
		terminal: !agent,
		created:  time.Now(),
		state:    taskSent,
		done:     make(chan struct{}),
	}
	reg := d.tasksRegistry()
	if busy := reg.addLive(t); busy != "" {
		return refuse("pane %s already has task %s in flight — wait for it (wait_task) or pick another pane", req.ToPane, busy)
	}
	if !d.deliverPrompt(pane, req.Prompt, agent) {
		reg.mu.Lock()
		// Guarded like every other transition: the task is published, so an
		// event arriving between add and here can already have ended it, and
		// a second close(t.done) panics the daemon.
		if !t.state.terminal() {
			t.state = taskFailed
			t.errText = "pane input queue is full — its child has stopped reading stdin"
			t.ended = time.Now()
			close(t.done)
		}
		info, errText := t.infoLocked(), t.errText
		reg.mu.Unlock()
		return ipc.DelegateTaskRespPayload{Task: info, Error: errText}
	}
	if req.TimeoutMs > 0 {
		id := t.id
		reg.arm(t, time.Duration(req.TimeoutMs)*time.Millisecond, func() {
			if tt := reg.get(id); tt != nil {
				d.finishTask(tt, taskTimeout, "")
			}
		})
	}
	log.Printf("task %s: %s → %s (%d bytes, notify=%v)", t.id, req.FromPane, req.ToPane, len(req.Prompt), t.notify)
	return ipc.DelegateTaskRespPayload{Task: reg.info(t)}
}

// taskObserve is called from emitEvent for EVERY event (ahead of the mute
// bypass) with the ledger transition that event produced. It advances every
// live task aimed at the pane.
func (d *Daemon) taskObserve(pane *Pane, e PaneEvent, tr hookevents.Transition) {
	reg := d.tasksRegistry()
	for _, t := range reg.all() {
		// t.to and t.terminal are written once, before the task is published;
		// t.state is not, so it is read through the registry lock.
		if t.to != pane.ID || !reg.live(t) {
			continue
		}
		switch {
		case e.Type == "process_exit":
			d.finishTask(t, taskFailed, "process exited")
		case t.terminal && e.Type == "command_complete":
			// A shell reports its own completion through OSC 133; the ledger
			// never moves for it.
			d.finishTask(t, taskDone, "")
		case !t.terminal && tr.Now == hookevents.WorkWorking:
			reg.advance(t, taskSent, taskWorking, e.Timestamp)
		case !t.terminal && tr.FellIdle:
			// Not done yet: the settle window decides. onPaneIdle runs the
			// closure after it, or never if the pane went back to work — in
			// which case the next FellIdle registers again. The closure
			// re-checks the state because two edges can register twice.
			//
			// An ABORT is not a completion: the pane's process exited, and the
			// process_exit branch above fails the task instead. Returning here
			// is what makes that outcome deterministic — both run, on
			// different goroutines, and whichever reaches finishTask first
			// decides the state a crashed pane is reported with.
			id := t.id
			d.onPaneIdle(pane.ID, func(aborted bool) {
				if aborted {
					return
				}
				if tt := reg.get(id); tt != nil && reg.live(tt) {
					d.finishTask(tt, taskDone, "")
				}
			})
		}
	}
}

// failTasksForPane ends every task aimed at a pane that is being destroyed.
//
// Nothing else can: the only path that advances a task is emitEvent, which
// looks the pane up in the session first — and every destroy path removes it
// there before (or instead of) the child's process_exit ever lands. Without
// this a delegated task whose target is closed stays `sent` forever, so
// wait_task can only keep timing out and no completion notice is ever
// delivered. Called from cleanupPaneArtifacts, the funnel every destroy path
// already goes through.
func (d *Daemon) failTasksForPane(paneID, reason string) {
	if paneID == "" {
		return
	}
	// Through tasksRegistry, never a bare read of d.tasks: the field is
	// published by a sync.Once from whichever goroutine delegated first, and
	// this runs on a destroy path that may never have touched tasks at all.
	for _, t := range d.tasksRegistry().liveTasksTo(paneID) {
		d.finishTask(t, taskFailed, reason)
	}
}

// finishTask moves t to a terminal state exactly once: captures the target's
// last output, queues task_done, wakes waiters and starts the notify-back.

func (d *Daemon) finishTask(t *task, st taskState, errText string) {
	// Resolve before the registry lock; never reacquire sm.mu under workMu.
	target := d.session.Pane(t.to)
	reg := d.tasksRegistry()
	reg.mu.Lock()
	if t.state.terminal() {
		reg.mu.Unlock()
		return
	}
	t.state = st
	t.errText = errText
	t.ended = time.Now()
	if t.timer != nil {
		t.timer.Stop()
	}
	if target != nil {
		t.result = paneOutputExcerpt(target, taskResultLines)
	}
	close(t.done)
	info := t.infoLocked()
	reg.mu.Unlock()

	log.Printf("task %s: %s (%s → %s)", t.id, st, t.from, t.to)
	target = d.session.Pane(t.to)
	ev := PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    t.to,
		Type:      "task_done",
		Title:     fmt.Sprintf("Task %s: %s", t.id, st),
		Message:   lastNonEmptyLine(t.result),
		Severity:  "info",
		Timestamp: time.Now(),
		Data: map[string]string{
			"task_id": t.id, "state": string(st), "from_pane": t.from, "to_pane": t.to,
			"excerpt": t.result,
		},
	}
	if st != taskDone {
		ev.Severity = "warning"
	}
	if target != nil {
		ev.TabID = target.TabID
		ev.PaneName = info.ToPaneName
	}
	d.emitEvent(ev)
	if t.notify {
		d.notifyRequester(t)
	}
}

// notifyRequester types the completion notice into the requester's prompt,
// but only while the requester is not mid-turn: text typed into a working
// agent is at best queued behind its current work and at worst treated as
// part of an answer. onPaneIdle delivers now or on the requester's next
// settled idle.
func (d *Daemon) notifyRequester(t *task) {
	from := d.session.Pane(t.from)
	if from == nil || !d.isAgentPane(from) {
		return
	}
	reg := d.tasksRegistry()
	line := taskNotice(t)
	// Delivered on an abort too: the requester's own process exiting is the
	// one edge that guarantees no idle edge is ever coming, and a subscriber
	// dropped there is a requester left waiting for a notice forever. If the
	// pane is really gone, deliverPrompt simply finds no process.
	d.onPaneIdle(t.from, func(bool) {
		p := d.session.Pane(t.from)
		if p == nil {
			return
		}
		if d.deliverPrompt(p, line, true) {
			reg.mu.Lock()
			t.notified = true
			reg.mu.Unlock()
		}
	})
}

// taskNotice is the one line the requester reads. It names the task, the
// pane and the state, quotes the target's last output line, and says which
// tool fetches the rest — an agent reading it knows what to call next.
func taskNotice(t *task) string {
	who := t.to
	if t.toName != "" {
		who = fmt.Sprintf("%s (%s)", t.to, t.toName)
	}
	last := truncateField(lastNonEmptyLine(t.result), taskNoticeMaxLine)
	msg := fmt.Sprintf("[quil task %s] pane %s %s.", t.id, who, t.state)
	if t.errText != "" {
		msg += " " + t.errText + "."
	}
	if last != "" {
		msg += " Last output: " + last
	}
	msg += fmt.Sprintf(" Call get_task with task_id=%s for the full result, or read_pane_output on %s.", t.id, t.to)
	return msg
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

func (d *Daemon) handleDelegateTaskReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.DelegateTaskReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgDelegateTaskResp, ipc.DelegateTaskRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	respondTo(conn, msg.ID, ipc.MsgDelegateTaskResp, d.delegateTask(req))
}

func (d *Daemon) handleGetTaskReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.GetTaskReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgGetTaskResp, ipc.GetTaskRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	t := d.tasksRegistry().get(req.TaskID)
	if t == nil {
		respondTo(conn, msg.ID, ipc.MsgGetTaskResp, ipc.GetTaskRespPayload{Error: "no such task: " + req.TaskID})
		return
	}
	respondTo(conn, msg.ID, ipc.MsgGetTaskResp, ipc.GetTaskRespPayload{Task: d.tasksRegistry().info(t)})
}

func (d *Daemon) handleWaitTaskReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.WaitTaskReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgWaitTaskResp, ipc.WaitTaskRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	reg := d.tasksRegistry()
	t := reg.get(req.TaskID)
	if t == nil {
		respondTo(conn, msg.ID, ipc.MsgWaitTaskResp, ipc.WaitTaskRespPayload{Error: "no such task: " + req.TaskID})
		return
	}
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 60000
	}
	if timeoutMs > 300000 {
		timeoutMs = 300000
	}
	// Off the dispatch goroutine, like watch_notifications: this blocks for
	// up to five minutes and the goroutine carries every message from the
	// requesting client.
	go func() {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-t.done:
			respondTo(conn, msg.ID, ipc.MsgWaitTaskResp, ipc.WaitTaskRespPayload{Task: reg.info(t)})
		case <-timer.C:
			respondTo(conn, msg.ID, ipc.MsgWaitTaskResp, ipc.WaitTaskRespPayload{Task: reg.info(t), Timeout: true})
		case <-d.shutdown:
		}
	}()
}

func (d *Daemon) handleListTasksReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.ListTasksReqPayload
	if len(msg.Payload) > 0 {
		_ = msg.DecodePayload(&req)
	}
	reg := d.tasksRegistry()
	var out []ipc.TaskInfo
	for _, t := range reg.all() {
		if req.PaneID != "" && t.to != req.PaneID && t.from != req.PaneID {
			continue
		}
		out = append(out, reg.info(t))
	}
	if out == nil {
		out = []ipc.TaskInfo{}
	}
	respondTo(conn, msg.ID, ipc.MsgListTasksResp, ipc.ListTasksRespPayload{Tasks: out})
}
