package tui

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// The accept branch of the kill confirm, end to end.
//
// This is the critical missing coverage the prior round's finding did NOT
// close. That round asked for the kill path to be reachable from its call site,
// and the test added for it stops at OPENING the confirm — so mutating the
// accept arm (`return m, m.sendKillProcess()` -> `return m, nil`) left the
// entire suite green. The user would press y, watch the confirm close, and
// discover nothing was stopped: a silent failure of the one destructive action
// in the dialog.
//
// Modelled on TestHandleConfirmKey_StopDaemonYSendsAndQuits, whose own doc
// calls this exact shape the critical missing coverage of ITS original PR.
// Unlike shutdown, the kill sends from inside a tea.Cmd, so the command is
// invoked and the wire message inspected.
func TestKillConfirm_YActuallySendsTheRequest(t *testing.T) {
	fake := &fakeSender{}
	m := procModel()
	m.client = fake

	// Put the cursor on a killable descendant and open the confirm.
	rows := m.procRows()
	var want procRow
	for i, r := range rows {
		if r.pid == 5219 {
			m.proc.cursor = i
			want = r
		}
	}
	opened, _ := m.handleDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = opened.(Model)
	if m.confirmKind != confirmKindKillProcess {
		t.Fatalf("K did not open the kill confirm (kind=%q)", m.confirmKind)
	}

	accepted, cmd := m.handleDialogKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	got := accepted.(Model)

	if got.dialog != dialogProcesses {
		t.Errorf("dialog = %v, want dialogProcesses (accept returns to the list)", got.dialog)
	}
	if cmd == nil {
		t.Fatal("accepting the confirm returned no command — nothing was sent, " +
			"and the dialog closed as though something had been")
	}
	cmd()

	if len(fake.sent) != 1 {
		t.Fatalf("fake.sent len = %d, want 1 kill request", len(fake.sent))
	}
	msg := fake.sent[0]
	if msg.Type != ipc.MsgKillProcessReq {
		t.Fatalf("sent[0].Type = %q, want %q", msg.Type, ipc.MsgKillProcessReq)
	}

	var payload ipc.KillProcessReqPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	// The identity the user was shown is what must travel: a different PID or a
	// missing start time turns an authorised kill into one the daemon refuses,
	// or worse, one aimed at a recycled PID.
	if payload.PID != 5219 {
		t.Errorf("PID = %d, want 5219", payload.PID)
	}
	if payload.PaneID != "pane-a" {
		t.Errorf("PaneID = %q, want pane-a", payload.PaneID)
	}
	if payload.StartMS != want.startMS {
		t.Errorf("StartMS = %d, want %d (the value the row displayed)",
			payload.StartMS, want.startMS)
	}
	if payload.StartMS == 0 {
		t.Error("StartMS is zero — the daemon refuses an unknown start time, so " +
			"this kill could never succeed")
	}
}

// Esc must send nothing. The confirm exists to make the destructive action
// deliberate; a cancel that still fires it would be worse than no confirm.
func TestKillConfirm_EscSendsNothing(t *testing.T) {
	fake := &fakeSender{}
	m := procModel()
	m.client = fake

	rows := m.procRows()
	for i, r := range rows {
		if r.pid == 5219 {
			m.proc.cursor = i
		}
	}
	opened, _ := m.handleDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = opened.(Model)

	cancelled, cmd := m.handleDialogKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		cmd()
	}
	if got := cancelled.(Model); got.dialog != dialogProcesses {
		t.Errorf("dialog = %v, want dialogProcesses", got.dialog)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgKillProcessReq {
			t.Fatal("cancelling the confirm still sent a kill request")
		}
	}
}

// Enter must send nothing either. It is the commit key everywhere else in the
// TUI, and this confirm is reached from a list the user is scrolling.
func TestKillConfirm_EnterSendsNothing(t *testing.T) {
	fake := &fakeSender{}
	m := procModel()
	m.client = fake

	rows := m.procRows()
	for i, r := range rows {
		if r.pid == 5219 {
			m.proc.cursor = i
		}
	}
	opened, _ := m.handleDialogKey(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = opened.(Model)

	after, cmd := m.handleDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		cmd()
	}
	if got := after.(Model); got.dialog != dialogConfirm {
		t.Errorf("Enter left the confirm (dialog=%v)", got.dialog)
	}
	for _, msg := range fake.sent {
		if msg.Type == ipc.MsgKillProcessReq {
			t.Fatal("Enter sent a kill request; only an explicit y may stop a process")
		}
	}
}
