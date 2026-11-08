package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// agentWTModel is a model showing the view with the given entries, in a
// project rooted at /proj.
func agentWTModel(entries ...ipc.AgentWorktreeInfo) Model {
	m := modelWithPanes(&PaneModel{ID: "pane-plain", CWD: "/proj"})
	m.cfg = config.Default()
	m.projects[0].RootDir = "/proj"
	m.dialog = dialogAgentWorktrees
	m.agentWT = agentWTView{entries: entries}
	return m
}

func wtInfo(id, repo, branch string) ipc.AgentWorktreeInfo {
	return ipc.AgentWorktreeInfo{ID: id, Kind: "worktree", Repo: repo, Path: "/w/" + id, Branch: branch,
		PaneID: "pane-x", PaneName: "claude", Source: "claude"}
}

// The active project's repository comes first.
func TestAgentWTDialog_ListsGroupedByRepoActiveFirst(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/other", "o1"), wtInfo("b", "/proj", "p1"))
	rows := m.visibleAgentWT()
	if len(rows) != 2 || rows[0].ID != "b" {
		t.Errorf("rows = %+v, want /proj's entry first", rows)
	}
}

// The view never claims the agent MADE it.
func TestAgentWTDialog_LabelSaysAppearedWhile(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	out := stripANSI(m.renderAgentWTDialog())
	if !strings.Contains(out, "while claude") || strings.Contains(strings.ToLower(out), "made by") {
		t.Errorf("list wording:\n%s", out)
	}
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	if out := stripANSI(m.renderConfirmDialog()); !strings.Contains(out, "Appeared while claude ran") {
		t.Errorf("confirm wording:\n%s", out)
	}
}

// The view's detail reuses the close dialog's rows: the file list, the
// worktree row and the local branch row; Enter sends the act by entry id
// with the branch shown.
func TestAgentWTDialog_DetailReusesCloseRows(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	fake := &fakeSender{}
	m.client = fake
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	m.applyAgentWTFacts(agentWTFactsMsg{Gen: m.confirmWorktreeGen, Resp: ipc.AgentWorktreeFactsRespPayload{
		ID: "a", Status: ipc.WorktreeStatus{Changes: 1, Files: []ipc.WorktreeFile{{Code: "??", Path: "x.txt"}},
			Branch: "feat/a", BranchDeletable: true, DefaultBranch: "master", BranchTip: "tip-1"},
	}})
	if out := stripANSI(m.renderConfirmDialog()); !strings.Contains(out, "x.txt") {
		t.Fatalf("the detail does not list the files:\n%s", out)
	}

	m = confirmPress(m, " ") // worktree
	m = confirmPress(m, "down")
	m = confirmPress(m, " ") // local branch
	fake.sent = nil
	mdl, cmd := m.handleConfirmKey(keyPressFor("enter"))
	m = mdl.(Model)
	runCmd(cmd)
	var req ipc.AgentWorktreeActReqPayload
	if !decodeSent(t, fake, ipc.MsgAgentWorktreeActReq, &req) {
		t.Fatal("Enter sent no act request")
	}
	if req.ID != "a" || !req.RemoveWorktree || !req.DeleteBranch || req.Confirmation.Branch != "feat/a" || req.Confirmation.Tip != "tip-1" {
		t.Errorf("request = %+v", req)
	}
	if m.dialog != dialogAgentWorktrees {
		t.Error("the confirm did not return to the list")
	}
}

// Review Focus 5: nothing is removed before the file list is on screen. Enter
// with the worktree ticked waits while the facts are still on their way.
func TestAgentWTDialog_EnterWaitsForTheFacts(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	fake := &fakeSender{}
	m.client = fake
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	fake.sent = nil

	m = confirmPress(m, " ")
	m = confirmPress(m, "enter")

	if len(fake.sent) != 0 || m.dialog != dialogConfirm {
		t.Errorf("Enter acted before the facts arrived (sent %d)", len(fake.sent))
	}
}

// A list request nobody answers ends with a reason, never an endless Loading.
func TestAgentWTDialog_TimeoutShowsReason(t *testing.T) {
	m := agentWTModel()
	m.agentWT.gen, m.agentWT.loading = "g1", true
	m.applyAgentWTListTimeout(agentWTListTimeoutMsg{gen: "g1"})
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "did not answer") {
		t.Errorf("no reason after the timeout:\n%s", out)
	}
}

// The box never outgrows a 24-row terminal, however long the record.
func TestAgentWTDialog_FitsTerminal(t *testing.T) {
	var entries []ipc.AgentWorktreeInfo
	for i := 0; i < 40; i++ {
		entries = append(entries, wtInfo(fmt.Sprintf("e%02d", i), fmt.Sprintf("/repo%d", i%3), fmt.Sprintf("feat/%02d", i)))
	}
	m := agentWTModel(entries...)
	m.width, m.height = 100, 24
	lines := strings.Count(m.renderAgentWTDialog(), "\n") + 1
	if box := lines + dialogVChrome; box > m.height {
		t.Errorf("box is %d rows in a %d-row terminal", box, m.height)
	}
}

// Gone entries are hidden until asked for.
func TestAgentWTDialog_HidesGoneByDefault(t *testing.T) {
	gone := wtInfo("g", "/proj", "old")
	gone.Gone = true
	m := agentWTModel(gone, wtInfo("a", "/proj", "feat/a"))
	if rows := m.visibleAgentWT(); len(rows) != 1 || rows[0].ID != "a" {
		t.Errorf("rows = %+v, want only the live entry", rows)
	}
	m = applyModel(m.handleAgentWTKey(keyPressFor("g")))
	if rows := m.visibleAgentWT(); len(rows) != 2 {
		t.Errorf("g did not show gone entries: %+v", rows)
	}
}

// A repository whose record may miss items says so, in one line, and the
// list still fits the terminal with that line added.
//
// Fails if the uncertain repositories are not shown, or the extra line
// pushes the footer off screen.
func TestAgentWTDialog_ShowsUncertainReposAndStillFits(t *testing.T) {
	var entries []ipc.AgentWorktreeInfo
	for i := 0; i < 40; i++ {
		entries = append(entries, wtInfo(fmt.Sprintf("e%02d", i), fmt.Sprintf("/repo%d", i%3), fmt.Sprintf("feat/%02d", i)))
	}
	m := agentWTModel(entries...)
	m.agentWT.uncertain = []string{"/repo1"}
	m.width, m.height = 100, 24

	out := m.renderAgentWTDialog()
	if !strings.Contains(stripANSI(out), "May miss items in /repo1") {
		t.Errorf("no uncertainty line:\n%s", stripANSI(out))
	}
	if box := strings.Count(out, "\n") + 1 + dialogVChrome; box > m.height {
		t.Errorf("box is %d rows in a %d-row terminal", box, m.height)
	}
	if !strings.Contains(stripANSI(out), "Esc close") {
		t.Error("the footer is not drawn")
	}
}

// An entry in a repository whose record may miss items says so, refuses
// Enter, and its `y` carries the acknowledgment the daemon requires.
//
// Fails if the uncertainty is display-only in the confirm.
func TestAgentWTDialog_UncertainRepoNeedsY(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	m.agentWT.uncertain = []string{"/proj"}
	fake := &fakeSender{}
	m.client = fake
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	m.applyAgentWTFacts(agentWTFactsMsg{Gen: m.confirmWorktreeGen, Resp: ipc.AgentWorktreeFactsRespPayload{
		ID: "a", Status: ipc.WorktreeStatus{Changes: 1, Files: []ipc.WorktreeFile{{Code: "??", Path: "x.txt"}}, Branch: "feat/a"},
	}})
	if out := stripANSI(m.renderConfirmDialog()); !strings.Contains(out, "may miss items") {
		t.Errorf("no uncertainty warning in the confirm:\n%s", out)
	}

	m = confirmPress(m, " ") // worktree
	fake.sent = nil
	m = confirmPress(m, "enter")
	if len(fake.sent) != 0 || m.dialog != dialogConfirm {
		t.Fatalf("Enter acted in an uncertain repository (sent %d)", len(fake.sent))
	}

	mdl, cmd := m.handleConfirmKey(keyPressFor("y"))
	_ = mdl
	runCmd(cmd)
	var req ipc.AgentWorktreeActReqPayload
	if !decodeSent(t, fake, ipc.MsgAgentWorktreeActReq, &req) || !req.AcceptUncertain || !req.RemoveWorktree {
		t.Errorf("request = %+v, want the removal with AcceptUncertain", req)
	}
}

// A refresh re-sorts the list and rows come and go; the cursor stays on the
// SAME entry, so a key pressed right after (f forgets with no confirm) acts
// on the row the user was looking at.
//
// Fails if the cursor keeps its index across a refresh.
func TestAgentWTDialog_RefreshKeepsTheCursorOnItsEntry(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "p1"), wtInfo("b", "/proj", "p2"))
	m.agentWT.gen = "g1"
	m.agentWT.cursor = 1 // "b"
	m.applyAgentWTList(agentWTListMsg{Gen: "g1", Resp: ipc.AgentWorktreesRespPayload{
		Entries: []ipc.AgentWorktreeInfo{wtInfo("new", "/proj", "p0"), wtInfo("a", "/proj", "p1"), wtInfo("b", "/proj", "p2")},
	}})
	if rows := m.visibleAgentWT(); rows[m.agentWT.cursor].ID != "b" {
		t.Errorf("cursor on %q after the refresh, want b", rows[m.agentWT.cursor].ID)
	}
}

// An act answered while the entry's confirm is (still) open refreshes the
// list too, or the acted-on row is still there when the user returns.
//
// Fails if the list refreshes only while the list itself is on screen.
func TestAgentWTDialog_ActAnswerRefreshesUnderTheConfirm(t *testing.T) {
	m := agentWTModel(wtInfo("a", "/proj", "feat/a"))
	fake := &fakeSender{}
	m.client = fake
	mdl, _ := m.openAgentWTConfirm(m.visibleAgentWT()[0])
	m = mdl.(Model)
	fake.sent = nil

	runCmd(m.applyAgentWTAct(agentWTActMsg{Resp: ipc.AgentWorktreeActRespPayload{ID: "a", Notice: "Worktree removed."}}))

	var req struct{}
	if !decodeSent(t, fake, ipc.MsgAgentWorktreesReq, &req) {
		t.Error("an act answered under the confirm did not refresh the list")
	}
}

// The list's keys: the cursor moves and stops at both ends; g shows the gone
// entries and resets the cursor; Enter on a gone entry only explains; f
// forgets the entry under the cursor, by id; Esc closes and clears the view.
func TestAgentWTDialog_ListKeys(t *testing.T) {
	gone := wtInfo("c", "/proj", "p3")
	gone.Gone = true
	m := agentWTModel(wtInfo("a", "/proj", "p1"), wtInfo("b", "/proj", "p2"), gone)
	fake := &fakeSender{}
	m.client = fake
	press := func(k string) tea.Cmd {
		mdl, cmd := m.handleAgentWTKey(keyPressFor(k))
		m = mdl.(Model)
		return cmd
	}

	press("down")
	press("down") // only two visible rows: stays on the last
	if m.agentWT.cursor != 1 {
		t.Errorf("cursor = %d after two downs, want 1", m.agentWT.cursor)
	}
	press("up")
	press("up")
	if m.agentWT.cursor != 0 {
		t.Errorf("cursor = %d after two ups, want 0", m.agentWT.cursor)
	}

	press("g")
	if !m.agentWT.showGone || len(m.visibleAgentWT()) != 3 || m.agentWT.cursor != 0 {
		t.Fatalf("after g: showGone=%v rows=%d cursor=%d", m.agentWT.showGone, len(m.visibleAgentWT()), m.agentWT.cursor)
	}
	for i, e := range m.visibleAgentWT() {
		if e.ID == "c" {
			m.agentWT.cursor = i
		}
	}
	press("enter")
	if !strings.Contains(m.agentWT.notice, "already gone") || m.dialog != dialogAgentWorktrees {
		t.Errorf("Enter on a gone entry: notice %q, dialog %v", m.agentWT.notice, m.dialog)
	}

	fake.sent = nil
	runCmd(press("f"))
	var req ipc.AgentWorktreeActReqPayload
	if !decodeSent(t, fake, ipc.MsgAgentWorktreeActReq, &req) || req.ID != "c" || !req.Forget || req.RemoveWorktree || req.DeleteBranch {
		t.Errorf("f sent %+v, want a forget of c only", req)
	}

	press("esc")
	if m.dialog != dialogNone || len(m.agentWT.entries) != 0 {
		t.Errorf("Esc left dialog %v with %d entries", m.dialog, len(m.agentWT.entries))
	}
}

// The view says what it is doing: loading, a failure, an empty record, the
// uncertain-repository warning (one repository by name, several by count),
// and a notice; rows name the kind, the pane, its state and whether it is
// gone or in use.
func TestAgentWTDialog_RenderStates(t *testing.T) {
	m := agentWTModel()
	m.width = 200
	m.agentWT.loading = true
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "Loading") {
		t.Errorf("loading:\n%s", out)
	}
	m.agentWT.loading = false
	m.agentWT.err = "the daemon did not answer"
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "did not answer") {
		t.Errorf("error:\n%s", out)
	}
	m.agentWT.err = ""
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "Nothing recorded") {
		t.Errorf("empty:\n%s", out)
	}

	m.agentWT.uncertain = []string{"/proj"}
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "May miss items in /proj") {
		t.Errorf("one uncertain repository:\n%s", out)
	}
	m.agentWT.uncertain = []string{"/proj", "/other"}
	if out := stripANSI(m.renderAgentWTDialog()); !strings.Contains(out, "2 repositories") {
		t.Errorf("several uncertain repositories:\n%s", out)
	}

	inUse := wtInfo("a", "/proj", "a")
	inUse.Source = "" // keep the row short: a long row is cut at the dialog width
	inUse.InUseBy = "pane-1"
	inUse.PaneLive = false
	inUse.FirstSeen = time.Now().Add(-3 * time.Hour).UnixMilli()
	branch := ipc.AgentWorktreeInfo{ID: "b", Kind: "branch", Repo: "/proj", Branch: "feat/b", FirstSeen: time.Now().Add(-72 * time.Hour).UnixMilli()}
	m.agentWT.entries = []ipc.AgentWorktreeInfo{inUse, branch}
	m.agentWT.uncertain = nil
	m.agentWT.notice = "Forgotten. Git is unchanged."
	out := stripANSI(m.renderAgentWTDialog())
	for _, want := range []string{"worktree a", "claude (closed)", "in use", "3h", "branch feat/b", "a pane", "3d", "Forgotten."} {
		if !strings.Contains(out, want) {
			t.Errorf("rows lack %q:\n%s", want, out)
		}
	}
}

// A list request that never answers says so instead of loading forever; a
// stale timeout (another request since) changes nothing. A refused act is
// shown as refused.
func TestAgentWTDialog_TimeoutAndRefusal(t *testing.T) {
	m := agentWTModel()
	m.agentWT.loading, m.agentWT.gen = true, "g2"
	m.applyAgentWTListTimeout(agentWTListTimeoutMsg{gen: "g1"})
	if !m.agentWT.loading {
		t.Error("a stale timeout ended the current request")
	}
	m.applyAgentWTListTimeout(agentWTListTimeoutMsg{gen: "g2"})
	if m.agentWT.loading || !strings.Contains(m.agentWT.err, "did not answer") {
		t.Errorf("timeout: loading=%v err=%q", m.agentWT.loading, m.agentWT.err)
	}

	m.dialog = dialogNone
	if cmd := m.applyAgentWTAct(agentWTActMsg{Resp: ipc.AgentWorktreeActRespPayload{Error: "the branch moved"}}); cmd != nil {
		t.Error("an act answer with the view closed requested a refresh")
	}
	if !strings.HasPrefix(m.agentWT.notice, "Refused: ") {
		t.Errorf("notice = %q, want a refusal", m.agentWT.notice)
	}
}
