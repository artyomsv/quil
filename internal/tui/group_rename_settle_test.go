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
	return updateNoWait(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

func wantGroups(t *testing.T, m Model, step string, want ...string) {
	t.Helper()
	if got := groupNames(m); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: groups = %v, want %v", step, got, want)
	}
}

// hostFrame is a shared frame from hostA: proj-2 filed under group, and the
// host's list.
func hostFrame(runID string, rev uint64, group string, groups ...string) WorkspaceStateMsg {
	f := sharedFrame(runID, rev, "proj-2", group, groups...)
	f.Dest = "hostA"
	return f
}

// An accepted rename of a host's group keeps the group the host's: the
// origin is never turned into the user's. A frame sent before the daemon
// applied it (still listing the old name) neither drops the new name nor
// adds the old one back.
func TestGroupRename_Accepted(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "Infra", "X", "Infra", "Y"))
	m.groups.Groups[1].Collapsed = true

	m = renameThroughDialog(t, m, "Infra", "Ops")
	id := lastGroupOpID(t, conn)
	wantGroups(t, m, "after the commit", "X", "Ops", "Y")
	if o := originOf(t, m, "Ops"); o != groupOriginHost {
		t.Errorf("origin %q while the daemon answers, want host", o)
	}
	m = updateNoWait(t, m, sharedFrame("r", 2, "proj-1", "Infra", "X", "Infra", "Y")) // in flight
	wantGroups(t, m, "after an in-flight frame", "X", "Ops", "Y")
	if m.groups.groupOf("", "proj-1") != 1 {
		t.Errorf("proj-1 in group %d after the in-flight frame, want Ops", m.groups.groupOf("", "proj-1"))
	}

	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: true}})
	m = updateNoWait(t, m, sharedFrame("r", 3, "proj-1", "Ops", "X", "Ops", "Y"))
	wantGroups(t, m, "after the accept", "X", "Ops", "Y")
	if o := originOf(t, m, "Ops"); o != groupOriginHost {
		t.Errorf("origin %q after the accept, want host", o)
	}
	if !m.groups.Groups[1].Collapsed {
		t.Error("the renamed group lost its collapsed state")
	}
	if len(m.groupRenames) != 0 {
		t.Errorf("%d renames still unsettled", len(m.groupRenames))
	}
}

// A refused rename puts the old name back on the same group, with the
// refusal flashed. Before, the new name was the user's for good and the
// next frame added the host's old name beside it.
func TestGroupRename_Refused(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "", "X", "Infra"))
	m.groups.Groups[1].Collapsed = true
	m = renameThroughDialog(t, m, "Infra", "Ops")
	id := lastGroupOpID(t, conn)

	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: false, Error: "rename: taken"}})

	wantGroups(t, m, "after the refusal", "X", "Infra")
	if !m.groups.Groups[1].Collapsed || originOf(t, m, "Infra") != groupOriginHost {
		t.Errorf("Infra came back as %+v, want the same group (collapsed, host)", m.groups.Groups[1])
	}
	if !strings.Contains(m.flashText, "refused") || !strings.Contains(m.flashText, "rename: taken") {
		t.Errorf("flash = %q, want the refusal", m.flashText)
	}
	m = updateNoWait(t, m, sharedFrame("r", 2, "proj-1", "", "X", "Infra"))
	wantGroups(t, m, "after the next frame", "X", "Infra")
}

// The daemon applied the rename, but its answer was lost with the link.
// That is not a refusal: nothing changes locally, and the first frame after
// the reattach — listing the new name — settles it on the same group, which
// keeps its place and collapsed state.
func TestGroupRename_LostReplyThenReattachWithTheNewName(t *testing.T) {
	m, _, remote := twoDestModel(t)
	m = updateNoWait(t, m, hostFrame("q", 1, "Infra", "X", "Infra", "Y"))
	m.groups.Groups[1].Collapsed = true
	m = renameThroughDialog(t, m, "Infra", "Ops")
	if countSent(remote, ipc.MsgGroupOp) != 1 {
		t.Fatal("setup: the rename was not sent to hostA")
	}

	out, _ := m.Update(linkLostMsg{dest: "hostA", err: errLinkLost})
	m = out.(Model)
	wantGroups(t, m, "after the link loss", "X", "Ops", "Y")

	m = updateNoWait(t, m, hostFrame("q2", 1, "Ops", "X", "Ops", "Y")) // the new connection
	wantGroups(t, m, "after the reattach", "X", "Ops", "Y")
	if !m.groups.Groups[1].Collapsed {
		t.Error("the renamed group lost its collapsed state: deleted and added again")
	}
	if m.groups.groupOf("hostA", "proj-2") != 1 {
		t.Errorf("proj-2 in group %d, want Ops", m.groups.groupOf("hostA", "proj-2"))
	}
	if len(m.groupRenames) != 0 {
		t.Errorf("%d renames still unsettled", len(m.groupRenames))
	}
}

// The same lost answer when the daemon never applied it: the first frame
// after the reattach still lists the old name, and the same group gets it
// back.
func TestGroupRename_LostReplyThenReattachWithTheOldName(t *testing.T) {
	m, _, _ := twoDestModel(t)
	m = updateNoWait(t, m, hostFrame("q", 1, "Infra", "X", "Infra", "Y"))
	m.groups.Groups[1].Collapsed = true
	m = renameThroughDialog(t, m, "Infra", "Ops")
	out, _ := m.Update(linkLostMsg{dest: "hostA", err: errLinkLost})
	m = out.(Model)

	m = updateNoWait(t, m, hostFrame("q2", 1, "Infra", "X", "Infra", "Y"))

	wantGroups(t, m, "after the reattach", "X", "Infra", "Y")
	if !m.groups.Groups[1].Collapsed {
		t.Error("Infra lost its collapsed state")
	}
}

// A rename made before the groups import is answered is held, and keeps its
// bookkeeping when it is replayed: the daemon's answer to the replayed op
// settles it.
func TestGroupRename_DeferredThenAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
		want string
	}{{"accepted", true, "Platform"}, {"refused", false, "Infra"}} {
		t.Run(tc.name, func(t *testing.T) {
			m, local, _ := importTestModel(t)
			m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", ""))
			_, importID := importPayload(t, local)
			m = renameThroughDialog(t, m, "Infra", "Platform")
			if n := countSent(local, ipc.MsgGroupOp); n != 0 {
				t.Fatalf("setup: group_op sent %d times before the import answer", n)
			}
			m = updateNoWait(t, m, sharedImportRespMsg{dest: "", id: importID, resp: ipc.SharedImportRespPayload{Answered: []string{ipc.ImportKindGroups}}})
			id := lastGroupOpID(t, local)

			m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id, resp: ipc.OpRespPayload{OK: tc.ok, Error: "rename: taken"}})

			if m.groups.indexOf(tc.want) < 0 {
				t.Errorf("groups = %v, want %s", groupNames(m), tc.want)
			}
			if other := map[bool]string{true: "Infra", false: "Platform"}[tc.ok]; m.groups.indexOf(other) >= 0 {
				t.Errorf("groups = %v, %s should be gone", groupNames(m), other)
			}
			if len(m.groupRenames) != 0 {
				t.Errorf("%d renames still unsettled", len(m.groupRenames))
			}
		})
	}
}

// A rename while a daemon listing the group has its link down is refused
// before anything changes: no op is sent, and the old name stays.
func TestGroupRename_LinkDownAtSend(t *testing.T) {
	m, _, remote := twoDestModel(t)
	m = updateNoWait(t, m, hostFrame("q", 1, "", "Infra"))
	out, _ := m.Update(linkLostMsg{dest: "hostA", err: errLinkLost})
	m = out.(Model)
	if m.linkDownReason("hostA") == "" {
		t.Fatal("setup: hostA's link is not down")
	}
	sentBefore := countSent(remote, ipc.MsgGroupOp)

	m = renameThroughDialog(t, m, "Infra", "Ops")

	wantGroups(t, m, "after the refused rename", "Infra")
	if n := countSent(remote, ipc.MsgGroupOp) - sentBefore; n != 0 {
		t.Errorf("%d group_op sent into the down link", n)
	}
	if !strings.Contains(m.flashText, "group not renamed") || !strings.Contains(m.flashText, "is disconnected") {
		t.Errorf("flash = %q, want the refusal naming the link", m.flashText)
	}
	if len(m.groupRenames) != 0 {
		t.Errorf("a refused rename left %d unsettled entries", len(m.groupRenames))
	}
}

// One rename per group in flight: renaming it back (A→B, B→A) before the
// first answer is refused, naming the host it waits for, and sends nothing.
func TestGroupRename_SecondRenameWhilePendingRefused(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	m = renameThroughDialog(t, m, "Infra", "Ops")

	m = renameThroughDialog(t, m, "Ops", "Infra")

	if n := countSent(conn, ipc.MsgGroupOp); n != 1 {
		t.Errorf("%d group_op sent, want the first rename alone", n)
	}
	wantGroups(t, m, "after the refused second rename", "Ops")
	if !strings.Contains(m.flashText, "rename still waiting for "+hostLabel("")) {
		t.Errorf("flash = %q, want the host the rename waits for", m.flashText)
	}
	if m.dialog != dialogNone {
		t.Errorf("the rename dialog stayed open (dialog %v)", m.dialog)
	}
}

// Answers are matched by request id. Renames whose names repeat (Infra→Ops,
// Ops→Infra, Infra→Ops, one after another) each own their id: a stale
// answer to the first, arriving while the third waits, settles nothing.
func TestGroupRename_AnswerMatchedByRequestID(t *testing.T) {
	m, conn := connectedTestModelCapturingSends(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	ok := ipc.OpRespPayload{OK: true}

	m = renameThroughDialog(t, m, "Infra", "Ops")
	id1 := lastGroupOpID(t, conn)
	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id1, resp: ok})
	m = renameThroughDialog(t, m, "Ops", "Infra")
	id2 := lastGroupOpID(t, conn)
	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id2, resp: ok})
	m = renameThroughDialog(t, m, "Infra", "Ops")
	id3 := lastGroupOpID(t, conn)
	if id1 == id3 || id2 == id3 {
		t.Fatalf("request ids repeat: %s %s %s", id1, id2, id3)
	}

	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id1, resp: ipc.OpRespPayload{OK: false, Error: "stale"}})
	wantGroups(t, m, "after a stale answer to the first rename", "Ops")
	if len(m.groupRenames) != 1 {
		t.Fatalf("%d renames pending, want the third", len(m.groupRenames))
	}

	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: id3, resp: ipc.OpRespPayload{OK: false, Error: "rename: taken"}})
	wantGroups(t, m, "after the third is refused", "Infra")
	if len(m.groupRenames) != 0 {
		t.Errorf("%d renames still pending", len(m.groupRenames))
	}
}

// Disconnecting the only host a rename waits for settles it at once: no
// frame from that host can come, nothing accepted it, so the old name is
// back on the same group.
func TestGroupRename_DisconnectOfTheOnlyPendingHostReverts(t *testing.T) {
	m, _, remote := twoDestModel(t)
	m = updateNoWait(t, m, hostFrame("q", 1, "", "X", "Infra"))
	m.groups.Groups[1].Collapsed = true
	m = renameThroughDialog(t, m, "Infra", "Ops")
	if countSent(remote, ipc.MsgGroupOp) != 1 {
		t.Fatal("setup: the rename was not sent to hostA")
	}

	m = confirmDisconnect(t, m, "hostA")

	if m.groups.indexOf("Ops") >= 0 || m.groups.indexOf("Infra") < 0 {
		t.Errorf("groups = %v, want Infra back", groupNames(m))
	}
	if g := m.groups.indexOf("Infra"); g >= 0 && !m.groups.Groups[g].Collapsed {
		t.Error("Infra came back as another group: the collapsed state is gone")
	}
	if len(m.groupRenames) != 0 {
		t.Errorf("%d renames still pending after the host left", len(m.groupRenames))
	}
}

// Of two hosts, one accepted and the other is disconnected before it
// answers: the rename stands.
func TestGroupRename_DisconnectAfterTheOtherHostAccepted(t *testing.T) {
	m, local, remote := twoDestModel(t)
	m = updateNoWait(t, m, sharedFrame("r", 1, "proj-1", "", "Infra"))
	m = updateNoWait(t, m, hostFrame("q", 1, "", "Infra"))
	m = renameThroughDialog(t, m, "Infra", "Ops")
	if countSent(local, ipc.MsgGroupOp) != 1 || countSent(remote, ipc.MsgGroupOp) != 1 {
		t.Fatal("setup: the rename did not reach both hosts")
	}
	m = updateNoWait(t, m, sharedOpRespMsg{dest: "", id: lastGroupOpID(t, local), resp: ipc.OpRespPayload{OK: true}})

	m = confirmDisconnect(t, m, "hostA")

	if m.groups.indexOf("Ops") < 0 {
		t.Errorf("groups = %v, want the accepted Ops", groupNames(m))
	}
	if len(m.groupRenames) != 0 {
		t.Errorf("%d renames still pending after the host left", len(m.groupRenames))
	}
}
