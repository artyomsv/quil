package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// renameThroughDialog renames group from to to as the user does: the
// dialog, its text, Enter — through Update.
func renameThroughDialog(t *testing.T, m Model, from, to string) Model {
	t.Helper()
	m.initKeymap()
	m.beginGroupEdit(groupEditState{mode: groupEditRename, target: from, input: to})
	return updateWith(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// lastGroupOpID is the id of the last group_op sent on conn.
func lastGroupOpID(t *testing.T, conn *fakeConn) string {
	t.Helper()
	msg := lastSent(t, conn, ipc.MsgGroupOp)
	if msg.ID == "" {
		t.Fatal("the group_op carries no id: its answer cannot be matched")
	}
	return msg.ID
}

func originOf(t *testing.T, m Model, name string) string {
	t.Helper()
	g := m.groups.indexOf(name)
	if g < 0 {
		t.Fatalf("no group %q (groups %v)", name, groupNames(m))
	}
	return m.groups.Groups[g].Origin
}

// An accepted rename of a host's group gives it back the host origin: the
// new name is the host's group, not one the user owns forever.
func TestGroupRename_AcceptedKeepsTheHostOrigin(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	if o := originOf(t, m, "Infra"); o != groupOriginHost {
		t.Fatalf("setup: Infra origin %q, want host", o)
	}

	m = renameThroughDialog(t, m, "Infra", "Ops")
	id := lastGroupOpID(t, conn)
	if o := originOf(t, m, "Ops"); o != groupOriginUser {
		t.Errorf("while the daemon answers: origin %q, want user (an in-flight frame must not take it)", o)
	}

	m = updateWith(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: true}})
	if o := originOf(t, m, "Ops"); o != groupOriginHost {
		t.Errorf("after the daemon accepted: origin %q, want host", o)
	}
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "", "Ops"))
	if got := groupNames(m); len(got) != 1 || got[0] != "Ops" {
		t.Errorf("groups = %v, want Ops alone", got)
	}
}

// A refused rename puts the old name back as it was, with the refusal
// flashed. Before, the new name stayed as the user's and was never cleaned
// up: the next frame added the host's old name beside it.
func TestGroupRename_RefusedPutsTheOldNameBack(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	m = renameThroughDialog(t, m, "Infra", "Ops")
	id := lastGroupOpID(t, conn)

	m = updateWith(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: false, Error: "rename: taken"}})

	if got := groupNames(m); len(got) != 1 || got[0] != "Infra" {
		t.Errorf("groups = %v, want Infra back", got)
	}
	if o := originOf(t, m, "Infra"); o != groupOriginHost {
		t.Errorf("origin %q, want host as before", o)
	}
	if !strings.Contains(m.flashText, "refused") || !strings.Contains(m.flashText, "rename: taken") {
		t.Errorf("flash = %q, want the refusal", m.flashText)
	}
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "", "Infra"))
	if got := groupNames(m); len(got) != 1 || got[0] != "Infra" {
		t.Errorf("after the next frame: groups = %v, want Infra alone", got)
	}
}

// A frame that still lists the old name can arrive before the refusal; the
// merge then adds the old name back as a second group. The refusal folds
// them into one again, with the old name.
func TestGroupRename_RefusedAfterAnInFlightFrameLeavesOneGroup(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateWith(t, m, sharedFrame("r", 1, "proj-1", "Infra", "Infra"))
	m = renameThroughDialog(t, m, "Infra", "Ops")
	id := lastGroupOpID(t, conn)
	m = updateWith(t, m, sharedFrame("r", 2, "proj-1", "Infra", "Infra")) // in flight

	m = updateWith(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: false, Error: "rename: taken"}})

	if got := groupNames(m); len(got) != 1 || got[0] != "Infra" {
		t.Fatalf("groups = %v, want Infra alone", got)
	}
	if m.groups.groupOf("", "proj-1") != 0 {
		t.Errorf("proj-1 in group %d, want Infra", m.groups.groupOf("", "proj-1"))
	}
}

// A rename while a daemon listing the group has its link down is refused
// before anything changes: no op is sent, and the old name stays.
func TestGroupRename_LinkDownRefusedBeforeAnyChange(t *testing.T) {
	m, _, remote := twoDestModel(t)
	rf := sharedFrame("q", 1, "proj-2", "", "Infra")
	rf.Dest = "hostA"
	m = updateWith(t, m, rf)
	out, _ := m.Update(linkLostMsg{dest: "hostA", err: errLinkLost})
	m = out.(Model)
	if m.linkDownReason("hostA") == "" {
		t.Fatal("setup: hostA's link is not down")
	}
	sentBefore := countSent(remote, ipc.MsgGroupOp)

	m = renameThroughDialog(t, m, "Infra", "Ops")

	if got := groupNames(m); len(got) != 1 || got[0] != "Infra" {
		t.Errorf("groups = %v, want Infra unchanged", got)
	}
	if n := countSent(remote, ipc.MsgGroupOp) - sentBefore; n != 0 {
		t.Errorf("%d group_op sent into the down link", n)
	}
	if !strings.Contains(m.flashText, "group not renamed") || !strings.Contains(m.flashText, "is disconnected") {
		t.Errorf("flash = %q, want the refusal naming the link", m.flashText)
	}
}

// A rename whose answer is lost with the link was accepted by no daemon this
// client knows of: the old name comes back.
func TestGroupRename_AnswerLostWithTheLinkPutsTheOldNameBack(t *testing.T) {
	m, _, remote := twoDestModel(t)
	rf := sharedFrame("q", 1, "proj-2", "", "Infra")
	rf.Dest = "hostA"
	m = updateWith(t, m, rf)
	m = renameThroughDialog(t, m, "Infra", "Ops")
	if countSent(remote, ipc.MsgGroupOp) != 1 {
		t.Fatal("setup: the rename was not sent to hostA")
	}

	out, _ := m.Update(linkLostMsg{dest: "hostA", err: errLinkLost})
	m = out.(Model)

	if got := groupNames(m); len(got) != 1 || got[0] != "Infra" {
		t.Errorf("groups = %v, want Infra back", got)
	}
	if o := originOf(t, m, "Infra"); o != groupOriginHost {
		t.Errorf("origin %q, want host as before", o)
	}
}
