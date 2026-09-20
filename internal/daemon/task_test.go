package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// recordingLiveSession is a live fake PTY that records every stdin write.
type recordingLiveSession struct {
	liveFakeSession
	mu     sync.Mutex
	writes []string
}

func newRecordingLiveSession() *recordingLiveSession {
	return &recordingLiveSession{liveFakeSession: liveFakeSession{done: make(chan struct{})}}
}

func (r *recordingLiveSession) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.writes = append(r.writes, string(p))
	r.mu.Unlock()
	return len(p), nil
}

func (r *recordingLiveSession) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.writes, "")
}

// agentPane spawns a claude-code pane on a recording live session.
func agentPane(t *testing.T, d *Daemon, name string) (*Pane, *recordingLiveSession) {
	t.Helper()
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.PluginMu.Lock()
	pane.Type = "claude-code"
	pane.Name = name
	pane.PluginMu.Unlock()
	sess := newRecordingLiveSession()
	if err := d.spawnPane(pane, sess, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	return pane, sess
}

func waitWrites(t *testing.T, s *recordingLiveSession, want string) {
	t.Helper()
	if !waitUntilTrue(t, func() bool { return strings.Contains(s.joined(), want) }, 2*time.Second) {
		t.Fatalf("stdin never received %q; got %q", want, s.joined())
	}
}

func TestDelegateTask_PastesPromptThenEnter(t *testing.T) {
	d := newTestDaemon(t)
	target, sess := agentPane(t, d, "worker")

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "run the tests\nthen report"})
	if resp.Error != "" || resp.Task.State != "sent" || !strings.HasPrefix(resp.Task.ID, "task-") {
		t.Fatalf("resp = %+v", resp)
	}
	waitWrites(t, sess, "\x1b[200~run the tests\nthen report\x1b[201~")
	waitWrites(t, sess, "\r")
	if strings.Index(sess.joined(), "\r") < strings.Index(sess.joined(), "\x1b[201~") {
		t.Fatalf("Enter arrived before the paste closed: %q", sess.joined())
	}
}

func TestDeliverPrompt_EmbeddedTerminator_CannotEscapePaste(t *testing.T) {
	d := newTestDaemon(t)
	p, session := agentPane(t, d, "worker")
	if !d.deliverPrompt(p, "prefix\x1b[201~\rsuffix\u009b201~tail", true) {
		t.Fatal("delivery refused")
	}
	want := "\x1b[200~prefix\rsuffixtail\x1b[201~"
	waitWrites(t, session, want)
	if got := session.joined(); strings.Count(got, "\x1b[201~") != 1 || strings.Contains(got, "\u009b201~") {
		t.Fatalf("paste escape survived: %q", got)
	}
}

func TestDelegateTask_RefusesWhatPaneInputRefuses(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	placeholder, _ := d.session.CreatePane(tab.ID, t.TempDir())
	placeholder.PreparingWorktree = "feat/x"

	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: placeholder.ID, Prompt: "x"}); r.Error == "" || !strings.Contains(r.Error, "worktree") {
		t.Fatalf("placeholder accepted: %+v", r)
	}
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: "pane-nope", Prompt: "x"}); r.Error == "" {
		t.Fatalf("unknown pane accepted: %+v", r)
	}
	target, _ := agentPane(t, d, "w")
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "  "}); r.Error == "" {
		t.Fatalf("empty prompt accepted: %+v", r)
	}
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, FromPane: target.ID, Prompt: "x"}); r.Error == "" {
		t.Fatalf("self-delegation accepted: %+v", r)
	}
}

func TestDelegateTask_CompletesOnSettledIdleNotRawStop(t *testing.T) {
	d := newTestDaemon(t)
	// The settle window has to satisfy two assertions pulling in opposite
	// directions: the sleep below must OUTLAST it (to prove a live subagent
	// holds the task open past the window), while the check immediately after
	// SubagentStop must run INSIDE it (to prove the raw falling edge does not
	// end the task on its own).
	//
	// At 30ms with an 80ms sleep the second one was a race: any scheduling
	// delay over 30ms and the task had already settled done, failing with
	// "task ended on the raw falling edge" — which is the opposite of what
	// happened. Reproduced roughly 1 run in 3 on master under package load.
	// The gap is now 200ms against a 400ms sleep, which no scheduler delay
	// that leaves the rest of the suite passing can close.
	const settle = 200 * time.Millisecond
	shortenIdleSettle(t, settle)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	task := reg.get(resp.Task.ID)

	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("after start: %s", st)
	}
	d.emitEvent(hookEvent(target, "hook.claude.SubagentStart", map[string]string{"agent_type": "qa"}))
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	time.Sleep(2 * settle)
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("Stop with a live subagent ended the task: %s", st)
	}
	d.emitEvent(hookEvent(target, "hook.claude.SubagentStop", map[string]string{"agent_type": "qa"}))
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("task ended on the raw falling edge, before the settle window: %s", st)
	}
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "done" }, 2*time.Second) {
		t.Fatalf("task never settled done: %+v", reg.info(task))
	}
	info := reg.info(task)
	if info.EndedAt == 0 || info.StartedAt == 0 {
		t.Fatalf("timestamps missing: %+v", info)
	}
	// Waited for, not sampled. The card is queued as a CONSEQUENCE of the
	// state flip, on the other side of it — so a test that polls until the
	// state reads "done" and then samples the queue is racing the publish it
	// is trying to observe.
	if !waitUntilTrue(t, func() bool { return hasEventType(d, target.ID, "task_done") }, 2*time.Second) {
		t.Fatal("task_done never queued")
	}
	select {
	case <-task.done:
	default:
		t.Fatal("done channel not closed")
	}
}

func TestDelegateTask_ResumeInsideSettleKeepsWorking(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 40*time.Millisecond)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	task := reg.get(resp.Task.ID)

	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	d.emitEvent(hookEvent(target, "hook.claude.PreToolUse", nil))
	time.Sleep(120 * time.Millisecond)
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("task ended although the agent resumed inside the window: %s", st)
	}
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "done" }, 2*time.Second) {
		t.Fatalf("task never finished after the second Stop: %+v", reg.info(task))
	}
}

func TestDelegateTask_ProcessExitFailsAndTimeoutTimesOut(t *testing.T) {
	d := newTestDaemon(t)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	r1 := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "a", TimeoutMs: 30})
	if !waitUntilTrue(t, func() bool { return reg.info(reg.get(r1.Task.ID)).State == "timeout" }, 2*time.Second) {
		t.Fatalf("timeout never fired: %+v", reg.info(reg.get(r1.Task.ID)))
	}
	r2 := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "b"})
	d.emitEvent(hookEvent(target, "process_exit", map[string]string{"exit_code": "1"}))
	info := reg.info(reg.get(r2.Task.ID))
	if info.State != "failed" || info.Error == "" {
		t.Fatalf("after exit: %+v", info)
	}
}

func TestDelegateTask_TerminalTargetCompletesOnCommandComplete(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, _ := d.session.CreatePane(tab.ID, t.TempDir())
	sess := newRecordingLiveSession()
	if err := d.spawnPane(pane, sess, false); err != nil {
		t.Fatal(err)
	}
	reg := d.tasksRegistry()
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: pane.ID, Prompt: "make test"})
	// CR, not LF: LF is echoed but not executed by PowerShell under ConPTY.
	waitWrites(t, sess, "make test\r")
	if strings.Contains(sess.joined(), "\x1b[200~") {
		t.Fatalf("a terminal got a bracketed paste: %q", sess.joined())
	}
	d.emitEvent(hookEvent(pane, "command_complete", map[string]string{"exit_code": "0"}))
	if st := reg.info(reg.get(resp.Task.ID)).State; st != "done" {
		t.Fatalf("after command_complete: %s", st)
	}
}

// The notify-back is typed into the requester only while it is not mid-turn;
// otherwise it waits for the requester's own settled idle.
func TestDelegateTask_NotifyBackWaitsForRequesterIdle(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	from, fromSess := agentPane(t, d, "orchestrator")
	to, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	// Requester is mid-turn (it is the one calling the tool, after all).
	d.emitEvent(hookEvent(from, "hook.claude.UserPromptSubmit", nil))
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: to.ID, FromPane: from.ID, Prompt: "do it", Notify: true})
	task := reg.get(resp.Task.ID)

	d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "done" }, 2*time.Second) {
		t.Fatalf("task never done: %+v", reg.info(task))
	}
	time.Sleep(60 * time.Millisecond)
	if strings.Contains(fromSess.joined(), "[quil task") {
		t.Fatalf("notice typed into a WORKING requester: %q", fromSess.joined())
	}
	if reg.info(task).Notified {
		t.Fatal("Notified reported before delivery")
	}
	// Requester finishes its turn → the deferred notice lands.
	d.emitEvent(hookEvent(from, "hook.claude.Stop", nil))
	waitWrites(t, fromSess, "[quil task "+task.id+"] pane "+to.ID+" (worker) done.")
	waitWrites(t, fromSess, "get_task with task_id="+task.id)
	if !waitUntilTrue(t, func() bool { return reg.info(task).Notified }, time.Second) {
		t.Fatal("Notified never reported")
	}
}

func TestDelegateTask_NotifyBackIsImmediateForAnIdleRequester(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	from, fromSess := agentPane(t, d, "orchestrator")
	to, _ := agentPane(t, d, "worker")

	// The requester has finished a turn of its own, so its ledger really says
	// idle — an UNKNOWN pane is not idle (see the test below).
	d.emitEvent(hookEvent(from, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(from, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return hasEventType(d, from.ID, "agent_idle") }, 2*time.Second) {
		t.Fatal("requester never settled idle")
	}
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: to.ID, FromPane: from.ID, Prompt: "do it", Notify: true})
	d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	waitWrites(t, fromSess, "[quil task "+resp.Task.ID+"]")
}

// A requester whose ledger has never moved is UNKNOWN, not idle: an AI pane
// whose hooks never loaded reports that while working. Typing the notice in
// there lands it in a live turn.
func TestDelegateTask_NotifyBackWaitsForAnUnknownRequester(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	from, fromSess := agentPane(t, d, "orchestrator")
	to, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	if got := d.buildPaneStatus(from).AgentState; got != "" {
		t.Fatalf("requester AgentState = %q, want empty (unknown)", got)
	}
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: to.ID, FromPane: from.ID, Prompt: "do it", Notify: true})
	d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return reg.info(reg.get(resp.Task.ID)).State == "done" }, 2*time.Second) {
		t.Fatalf("task never done: %+v", reg.info(reg.get(resp.Task.ID)))
	}
	time.Sleep(60 * time.Millisecond)
	if strings.Contains(fromSess.joined(), "[quil task") {
		t.Fatalf("notice typed into an UNKNOWN-state requester: %q", fromSess.joined())
	}
	// Deferred, not dropped: the requester's own idle edge delivers it.
	d.emitEvent(hookEvent(from, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(from, "hook.claude.Stop", nil))
	waitWrites(t, fromSess, "[quil task "+resp.Task.ID+"]")
}

// One live task per target pane. Completion is read off the TARGET's ledger,
// which says nothing about which prompt finished — so with two tasks queued to
// one pane the first settled idle marked both done, including the one whose
// prompt the agent had not looked at yet.
func TestDelegateTask_RefusesASecondTaskForTheSamePane(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	target, _ := agentPane(t, d, "worker")
	other, _ := agentPane(t, d, "worker2")
	reg := d.tasksRegistry()

	first := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "one"})
	if first.Error != "" {
		t.Fatalf("first: %+v", first)
	}
	second := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "two"})
	if second.Error == "" || !strings.Contains(second.Error, first.Task.ID) {
		t.Fatalf("second task accepted or refusal does not name the live task: %+v", second)
	}
	if second.Task.ID != "" {
		t.Fatalf("a refused delegate still minted a task: %+v", second.Task)
	}
	// Another pane is unaffected.
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: other.ID, Prompt: "x"}); r.Error != "" {
		t.Fatalf("a second pane was refused: %+v", r)
	}
	// Once the first ends, the pane takes work again.
	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return reg.info(reg.get(first.Task.ID)).State == "done" }, 2*time.Second) {
		t.Fatalf("first never finished: %+v", reg.info(reg.get(first.Task.ID)))
	}
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "three"}); r.Error != "" {
		t.Fatalf("pane refused after its task ended: %+v", r)
	}
}

// A process exit must WIN over a pending completion. The abort releases the
// pane's idle subscribers on their own goroutine while taskObserve fails the
// task on the emitting one, so without the aborted flag whichever got there
// first decided whether a crashed pane was reported as done.
//
// The abort is driven through applyWorkEvent DIRECTLY, on purpose: emitEvent
// would also run taskObserve, which fails the task on the caller's goroutine
// and would mask which of the two answered. What is under test is the
// subscriber alone — the end-to-end outcome is asserted after it.
func TestDelegateTask_ProcessExitBeatsAPendingCompletion(t *testing.T) {
	d := newTestDaemon(t)
	// Long enough that the settle window cannot close by itself: the task's
	// completion callback is registered and waiting when the exit lands.
	shortenIdleSettle(t, 5*time.Second)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	if resp.Error != "" {
		t.Fatalf("delegate: %+v", resp)
	}
	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))

	d.applyWorkEvent(target, hookEvent(target, "process_exit", map[string]string{"exit_code": "1"}))
	time.Sleep(150 * time.Millisecond) // the subscribers run on their own goroutine
	if got := reg.info(reg.get(resp.Task.ID)); got.State == "done" {
		t.Fatalf("a crashed pane's task was completed by its idle subscriber: %+v", got)
	}
	// End to end, the exit is what ends it, and it ends it as failed.
	d.emitEvent(hookEvent(target, "process_exit", map[string]string{"exit_code": "1"}))
	if got := reg.info(reg.get(resp.Task.ID)); got.State != "failed" {
		t.Fatalf("after the exit: %+v", got)
	}
}

// The abort must also beat a completion callback that registers AFTER it.
//
// emitEvent applies an event to the ledger and observes it for tasks in two
// steps, so two goroutines emitting concurrently interleave as apply(Stop),
// apply(exit), observe(Stop), observe(exit) — the exact order driven here. The
// abort drains the subscriber list before the Stop's callback joins it, and
// the ledger then reports plain WorkIdle, so the late callback used to run
// immediately as a completion and mark a crashed pane done.
func TestDelegateTask_AbortBeatsACompletionRegisteredAfterIt(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 5*time.Second)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	if resp.Error != "" {
		t.Fatalf("delegate: %+v", resp)
	}
	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))

	stop := hookEvent(target, "hook.claude.Stop", nil)
	exit := hookEvent(target, "process_exit", map[string]string{"exit_code": "1"})
	trStop := d.applyWorkEvent(target, stop)
	trExit := d.applyWorkEvent(target, exit)
	d.taskObserve(target, stop, trStop)
	d.taskObserve(target, exit, trExit)

	if got := reg.info(reg.get(resp.Task.ID)); got.State != "failed" {
		t.Fatalf("a crashed pane reported %q: %+v", got.State, got)
	}
}

// Destroying a pane ends the tasks aimed at it. Nothing else can: the state
// machine only advances on events for a pane the session still holds, and
// every destroy path removes it from the session first — so the task sat at
// `sent` forever and wait_task could only keep timing out.
func TestDelegateTask_DestroyingTheTargetFailsItsTasks(t *testing.T) {
	d := newTestDaemon(t)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	if resp.Error != "" {
		t.Fatalf("delegate: %+v", resp)
	}
	msg, err := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: target.ID})
	if err != nil {
		t.Fatal(err)
	}
	d.handleDestroyPane(msg)

	info := reg.info(reg.get(resp.Task.ID))
	if info.State != "failed" || !strings.Contains(info.Error, "destroyed") {
		t.Fatalf("task after its target was destroyed: %+v", info)
	}
	if info.EndedAt == 0 {
		t.Fatalf("no end timestamp: %+v", info)
	}
	select {
	case <-reg.get(resp.Task.ID).done:
	default:
		t.Fatal("a waiter would still be blocked")
	}
}

// The timeout timer is published under the registry lock. The prompt is queued
// BEFORE the timer is armed, so a shell that answers at once has finishTask
// reading and stopping t.timer while delegateTask assigns it. Run with -race:
// the two accesses have no happens-before edge without the fix.
func TestDelegateTask_TimeoutTimerIsPublishedUnderTheLock(t *testing.T) {
	d := newTestDaemon(t)
	reg := d.tasksRegistry()

	for i := 0; i < 20; i++ {
		tab := d.session.CreateTab("t")
		pane, err := d.session.CreatePane(tab.ID, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := d.spawnPane(pane, newRecordingLiveSession(), false); err != nil {
			t.Fatalf("spawnPane: %v", err)
		}
		// A terminal target completes on command_complete, which needs no
		// ledger edge — so the completion can land in the window between the
		// prompt being queued and the timer being armed.
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d.emitEvent(hookEvent(pane, "command_complete", map[string]string{"exit_code": "0"}))
				}
			}
		}()
		resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: pane.ID, Prompt: "make test", TimeoutMs: 20})
		close(stop)
		wg.Wait()
		if resp.Error != "" {
			t.Fatalf("delegate: %+v", resp)
		}
		// A task the shell already finished must never be flipped to timeout by
		// a timer armed after the fact.
		task := reg.get(resp.Task.ID)
		if !waitUntilTrue(t, func() bool { return taskState(reg.info(task).State).terminal() }, 2*time.Second) {
			t.Fatalf("run %d: task never ended: %+v", i, reg.info(task))
		}
		first := reg.info(task).State
		time.Sleep(40 * time.Millisecond) // outlive the timeout it was given
		if got := reg.info(task).State; got != first {
			t.Fatalf("run %d: terminal state %q was overwritten with %q", i, first, got)
		}
	}
}

// Every read of task.state happens under the registry lock, and a terminal
// state can never be overwritten. Run with -race: the observer reads state on
// the emitting goroutine while timeouts and completions write it on theirs,
// and an unguarded sent→working write can resurrect a timed-out task, whose
// next completion closes its done channel a second time and panics.
func TestDelegateTask_StateTransitionsAreAtomic(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, time.Millisecond)
	reg := d.tasksRegistry()

	for i := 0; i < 20; i++ {
		target, _ := agentPane(t, d, "worker")
		resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "x", TimeoutMs: 1})
		if resp.Error != "" {
			t.Fatalf("delegate: %+v", resp)
		}
		var wg sync.WaitGroup
		for _, ev := range []string{"hook.claude.UserPromptSubmit", "hook.claude.Stop", "hook.claude.PreToolUse", "hook.claude.Stop"} {
			wg.Add(1)
			go func(typ string) {
				defer wg.Done()
				d.emitEvent(hookEvent(target, typ, nil))
			}(ev)
		}
		wg.Wait()
		if !waitUntilTrue(t, func() bool { return taskState(reg.info(reg.get(resp.Task.ID)).State).terminal() }, 2*time.Second) {
			t.Fatalf("task never reached a terminal state: %+v", reg.info(reg.get(resp.Task.ID)))
		}
	}
}

func TestTaskRegistry_EvictsOldestTerminalOnly(t *testing.T) {
	r := newTaskRegistry(2)
	live := &task{id: "a", to: "pane-1", state: taskWorking, done: make(chan struct{})}
	old := &task{id: "b", to: "pane-2", state: taskDone, done: make(chan struct{})}
	r.addLive(live)
	r.addLive(old)
	r.addLive(&task{id: "c", to: "pane-3", state: taskSent, done: make(chan struct{})})
	if r.get("b") != nil {
		t.Fatal("terminal task b not evicted")
	}
	if r.get("a") == nil || r.get("c") == nil {
		t.Fatal("a live task was evicted")
	}
}

func TestTaskRequests_RoundTrip(t *testing.T) {
	d, client := mcpTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	to, _ := agentPane(t, d, "worker")

	del := decodeInto[ipc.DelegateTaskRespPayload](t, roundTrip(t, client, ipc.MsgDelegateTaskReq, ipc.MsgDelegateTaskResp,
		ipc.DelegateTaskReqPayload{ToPane: to.ID, Prompt: "hello"}))
	if del.Error != "" || del.Task.ID == "" {
		t.Fatalf("delegate: %+v", del)
	}
	got := decodeInto[ipc.GetTaskRespPayload](t, roundTrip(t, client, ipc.MsgGetTaskReq, ipc.MsgGetTaskResp,
		ipc.GetTaskReqPayload{TaskID: del.Task.ID}))
	if got.Task.State != "sent" || got.Task.ToPaneName != "worker" {
		t.Fatalf("get: %+v", got)
	}
	list := decodeInto[ipc.ListTasksRespPayload](t, roundTrip(t, client, ipc.MsgListTasksReq, ipc.MsgListTasksResp,
		ipc.ListTasksReqPayload{PaneID: to.ID}))
	if len(list.Tasks) != 1 {
		t.Fatalf("list: %+v", list)
	}
	none := decodeInto[ipc.ListTasksRespPayload](t, roundTrip(t, client, ipc.MsgListTasksReq, ipc.MsgListTasksResp,
		ipc.ListTasksReqPayload{PaneID: "pane-other"}))
	if len(none.Tasks) != 0 {
		t.Fatalf("list for another pane: %+v", none)
	}
	timed := decodeInto[ipc.WaitTaskRespPayload](t, roundTrip(t, client, ipc.MsgWaitTaskReq, ipc.MsgWaitTaskResp,
		ipc.WaitTaskReqPayload{TaskID: del.Task.ID, TimeoutMs: 50}))
	if !timed.Timeout {
		t.Fatalf("wait on a live task did not time out: %+v", timed)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
		d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	}()
	done := decodeInto[ipc.WaitTaskRespPayload](t, roundTrip(t, client, ipc.MsgWaitTaskReq, ipc.MsgWaitTaskResp,
		ipc.WaitTaskReqPayload{TaskID: del.Task.ID, TimeoutMs: 3000}))
	if done.Timeout || done.Task.State != "done" {
		t.Fatalf("wait: %+v", done)
	}
	missing := decodeInto[ipc.GetTaskRespPayload](t, roundTrip(t, client, ipc.MsgGetTaskReq, ipc.MsgGetTaskResp,
		ipc.GetTaskReqPayload{TaskID: "task-nope"}))
	if missing.Error == "" {
		t.Fatal("unknown task answered without an error")
	}
}

// codexEnterSuppressWindow is codex's own PASTE_ENTER_SUPPRESS_WINDOW
// (codex-rs/tui/src/bottom_pane/paste_burst.rs) plus its Windows burst flush
// (PASTE_BURST_ACTIVE_IDLE_TIMEOUT). An Enter delivered inside that window is
// appended to the prompt as a newline instead of submitting it, and extends
// the window again — so the prompt sits in the composer and nothing runs.
const codexEnterSuppressWindow = 180 * time.Millisecond

// This guards a NUMBER rather than a behaviour on purpose: the failure it
// prevents is invisible from inside the daemon. The daemon queues the paste
// and the CR, both succeed, the task is recorded as sent, and the target
// simply never starts. Nothing in this process can observe that, so the only
// place the constraint can be written down is here, against the window it
// has to clear.
func TestPasteSettle_ClearsCodexEnterSuppressWindow(t *testing.T) {
	if pasteSettle <= codexEnterSuppressWindow {
		t.Fatalf("pasteSettle is %v; codex swallows an Enter delivered within %v of the paste", pasteSettle, codexEnterSuppressWindow)
	}
}
