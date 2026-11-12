package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

// A prompt the agent never starts on ends the task — and frees the pane for
// the next delegation. Before the start deadline such a task stayed `sent`
// forever and refused every later task to that pane (27 hours, 2026-11-10/11).
func TestDelegateTask_NeverStartedFailsAtTheStartDeadline(t *testing.T) {
	d := newTestDaemon(t)
	d.taskStartTimeout = 50 * time.Millisecond
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "review it"})
	if resp.Error != "" {
		t.Fatalf("delegate: %+v", resp)
	}
	task := reg.get(resp.Task.ID)
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "failed" }, 2*time.Second) {
		t.Fatalf("the start deadline never fired: %+v", reg.info(task))
	}
	if got := reg.info(task).Error; !strings.Contains(got, "did not start working on the prompt") {
		t.Errorf("error = %q, want the not-started reason", got)
	}
	if !waitUntilTrue(t, func() bool { return hasEventType(d, target.ID, "task_done") }, 2*time.Second) {
		t.Error("no task_done card for the failed task")
	}
	next := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "again"})
	if next.Error != "" {
		t.Errorf("the pane still refused a new task after the old one failed: %s", next.Error)
	}
}

// A task whose turn started is not failed by the start deadline: the deadline
// is about a prompt that was never taken, not about how long the work runs.
func TestDelegateTask_StartedTaskOutlivesTheStartDeadline(t *testing.T) {
	d := newTestDaemon(t)
	d.taskStartTimeout = 50 * time.Millisecond
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "long job"})
	task := reg.get(resp.Task.ID)
	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	time.Sleep(200 * time.Millisecond)
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("state = %s after the start deadline, want working", st)
	}
}

// A shell reports no start edge, so a terminal task has no start deadline.
func TestDelegateTask_TerminalTargetHasNoStartDeadline(t *testing.T) {
	d := newTestDaemon(t)
	d.taskStartTimeout = 50 * time.Millisecond
	tab := d.session.CreateTab("t")
	pane, _ := d.session.CreatePane(tab.ID, t.TempDir())
	if err := d.spawnPane(pane, newRecordingLiveSession(), false); err != nil {
		t.Fatal(err)
	}
	reg := d.tasksRegistry()
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: pane.ID, Prompt: "make test"})
	time.Sleep(200 * time.Millisecond)
	if st := reg.info(reg.get(resp.Task.ID)).State; st != "sent" {
		t.Fatalf("state = %s, want sent (a terminal task waits for command_complete)", st)
	}
}

// The timing is per daemon: shortening it on one leaves every other daemon on
// the production value.
func TestDelegateTask_StartTimeoutIsPerDaemon(t *testing.T) {
	short := newTestDaemon(t)
	short.taskStartTimeout = time.Millisecond
	other := newTestDaemon(t)
	if other.taskStartTimeout != defaultTaskStartTimeout {
		t.Errorf("a second daemon got %v, want the default %v", other.taskStartTimeout, defaultTaskStartTimeout)
	}
}

// Delegating to a deferred AI pane boots it and REFUSES, rather than pasting
// the prompt into an agent that is still loading — which is how the 27-hour
// task was made. Nothing is typed into it and no task is held against it.
//
// The retry the refusal asks for must be REACHABLE. A booted agent waiting
// for its first prompt reports no agent_state: its startup event is not a
// work edge. Codex is the case the review named; claude behaves the same. So
// the test feeds only the startup event, asserts agent_state is still
// unknown, and retries: the retry is accepted, and the refusal points at the
// screen, never at agent_state idle.
func TestDelegateTask_DeferredAgentPaneIsStartedAndRefused(t *testing.T) {
	for _, tc := range []struct {
		typ, startup string
		// register loads the plugin. LoadFromDir PRUNES every plugin not in
		// the directory it loads, so each case loads exactly its own: adding
		// codex on top of newTestDaemon's claude-code would unregister claude.
		register func(*testing.T, *Daemon)
	}{
		{"claude-code", "hook.claude.SessionStart", registerClaudePlugin},
		{"codex", "hook.codex.SessionStart", registerCodexPlugin},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			d := newTestDaemon(t)
			tc.register(t, d)
			// AFTER newTestDaemon, which installs and restores its own seam.
			sess := newRecordingLiveSession()
			prev := newSessionFn
			newSessionFn = func(cols, rows int) apty.Session { return sess }
			t.Cleanup(func() { newSessionFn = prev })

			tab := d.session.CreateTab("t")
			pane, err := d.session.CreatePane(tab.ID, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			pane.PluginMu.Lock()
			pane.Type = tc.typ
			pane.PluginMu.Unlock()
			pane.Pending.Store(true)

			resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: pane.ID, Prompt: "review it"})
			if !strings.Contains(resp.Error, "starting now") {
				t.Fatalf("resp = %+v, want the starting-now refusal", resp)
			}
			if !strings.Contains(resp.Error, "read_pane_output") || strings.Contains(resp.Error, "agent_state is idle") {
				t.Errorf("refusal %q must name the screen as the retry signal, not agent_state idle", resp.Error)
			}
			if pane.Pending.Load() {
				t.Error("the deferred pane was not started")
			}
			time.Sleep(pasteSettle + 100*time.Millisecond)
			if got := sess.joined(); strings.Contains(got, "review it") {
				t.Errorf("the prompt was typed into the loading agent: %q", got)
			}
			for _, task := range d.tasksRegistry().all() {
				if task.to == pane.ID {
					t.Errorf("a task %s was registered against the refused pane", task.id)
				}
			}

			// Only the startup event: the agent is ready and has had no prompt.
			d.emitEvent(hookEvent(pane, tc.startup, nil))
			if st, _, _ := paneWorkState(pane); st != "" {
				t.Fatalf("agent_state = %q after only the startup event, want unknown (\"\")", st)
			}
			again := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: pane.ID, Prompt: "review it"})
			if again.Error != "" {
				t.Fatalf("retry with agent_state unknown refused: %s", again.Error)
			}
			waitWrites(t, sess, "review it")
		})
	}
}
