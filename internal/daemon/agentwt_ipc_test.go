package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// daemonWithRecord builds a test daemon whose agent-worktree record holds
// the given entries, with compares run synchronously on an empty repo.
func daemonWithRecord(t *testing.T, entries ...AgentWTEntry) *Daemon {
	t.Helper()
	d := newTestDaemon(t)
	s, _ := newTestAgentWT(t, &fakeRepo{}, nil)
	s.paneFn = d.paneForAgentWT
	s.entries = append(s.entries, entries...)
	d.agentWT = s
	return d
}

var wtEntry = AgentWTEntry{ID: "e-wt", Kind: agentWTKindWorktree, Repo: "/repo", Path: "/w/x", Branch: "x",
	FirstSeen: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), PaneID: "pane-gone", PaneName: "claude"}

var brEntry = AgentWTEntry{ID: "e-br", Kind: agentWTKindBranch, Repo: "/repo", Branch: "feat/plain",
	FirstSeen: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)}

func TestAgentWTAct_UnknownIDRefused(t *testing.T) {
	d := daemonWithRecord(t)
	if got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "nope", RemoveWorktree: true}); got.Error == "" {
		t.Error("an unknown id was acted on")
	}
}

// git no longer lists the path: nothing reaches `worktree remove --force`.
func TestAgentWTAct_PathNoLongerListedRefused(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if got.Error == "" || len(*calls) != 0 {
		t.Errorf("resp = %+v, removals %v; want a refusal and no removal", got, *calls)
	}
}

// The folder now belongs to another repository: refused.
func TestAgentWTAct_RepoMismatchRefused(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/other", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if got.Error == "" || len(*calls) != 0 {
		t.Errorf("resp = %+v, removals %v; want a refusal", got, *calls)
	}
}

// A record naming the main checkout is never removed.
func TestAgentWTAct_MainCheckoutRefused(t *testing.T) {
	main := wtEntry
	main.Path = "/repo"
	d := daemonWithRecord(t, main)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if got.Error == "" || len(*calls) != 0 {
		t.Errorf("resp = %+v, removals %v; want a refusal for the main checkout", got, *calls)
	}
}

// A live pane in the worktree blocks the removal.
func TestAgentWTAct_InUseRefused(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	tab := d.session.CreateTab("t")
	if _, err := d.session.CreatePane(tab.ID, "/w/x"); err != nil {
		t.Fatal(err)
	}
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if got.Error == "" || len(*calls) != 0 {
		t.Errorf("resp = %+v, removals %v; want a refusal while a pane is in it", got, *calls)
	}
}

// Verified, unused: the removal runs on the recorded path, in the recorded
// repository.
func TestAgentWTAct_RemovesTheRecordedPath(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if got.Error != "" || len(*calls) != 1 || (*calls)[0][1] != "/w/x" {
		t.Errorf("resp = %+v, removals %v; want one removal of /w/x", got, *calls)
	}
}

// A removal git refuses (a folder another process holds) must come back as a
// refusal, not "started": the view would otherwise show a live row the user
// believes is gone.
//
// Fails if agentWorktreeAct ignores the removal's result.
func TestAgentWTAct_RemovalFailureIsReported(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	stubRemoveWorktree(t, func(context.Context, string, string) error {
		return errors.New("The process cannot access the file because it is being used by another process.")
	})

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if got.Error == "" || !strings.Contains(got.Error, "not removed") {
		t.Errorf("resp = %+v, want a refusal naming the failed removal", got)
	}
}

// A branch entry's local delete runs on the branch git lists, once
// confirmed, and reports on a card.
func TestAgentWTAct_BranchEntryDeletesLocally(t *testing.T) {
	d := daemonWithRecord(t, brEntry)
	prevList, prevFor, prevDef, prevLocal := branchListFn, branchFactsForFn, defaultBranchRefFn, deleteLocalBranchFn
	branchListFn = func(context.Context, string) ([]gitworktree.Branch, bool, error) {
		return []gitworktree.Branch{{Name: "master"}, {Name: "feat/plain"}}, false, nil
	}
	branchFactsForFn = func(_ context.Context, _, b, _ string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: b, DefaultRef: "refs/heads/master"}, nil
	}
	defaultBranchRefFn = func(context.Context, string) string { return "refs/heads/master" }
	var deleted []string
	deleteLocalBranchFn = func(_ context.Context, _, b string) error { deleted = append(deleted, b); return nil }
	t.Cleanup(func() {
		branchListFn, branchFactsForFn, defaultBranchRefFn, deleteLocalBranchFn = prevList, prevFor, prevDef, prevLocal
	})

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-br", DeleteBranch: true, Confirmation: ipc.BranchConfirmation{Branch: "feat/plain"}})

	if got.Error != "" || len(deleted) != 1 || deleted[0] != "feat/plain" {
		t.Errorf("resp = %+v, deleted %v; want feat/plain deleted", got, deleted)
	}
	if done := eventsOfType(d, "worktree_branch_deleted"); len(done) != 1 {
		t.Errorf("deleted cards = %+v, want one", done)
	}
}

// Forget drops the entry; the baseline is untouched.
func TestAgentWTAct_ForgetKeepsBaseline(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	d.agentWT.repos["/repo"] = &agentWTRepo{Worktrees: []string{"/w/x"}}

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", Forget: true})

	if got.Error != "" || len(d.agentWT.entriesSnapshot()) != 0 {
		t.Errorf("resp = %+v, entries %+v; want forgotten", got, d.agentWT.entriesSnapshot())
	}
	if r := d.agentWT.repos["/repo"]; r == nil || len(r.Worktrees) != 1 {
		t.Error("forget dropped the baseline, so the next compare would record it again")
	}
}

// The list answer says whether the pane still exists.
func TestAgentWorktreesResponse_ReportsPaneLive(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	got := d.agentWorktreesResponse()
	if len(got.Entries) != 1 || got.Entries[0].PaneLive || got.Entries[0].ID != "e-wt" {
		t.Errorf("entries = %+v, want one with a gone pane", got.Entries)
	}
}

// The cleanup view's facts keep the file list when the branch half runs out
// of its budget: the view waits as briefly as the close dialog.
//
// Fails if agentWorktreeFacts reads branch facts on the full request timeout.
func TestAgentWorktreeFacts_BranchBudgetKeepsTheFileList(t *testing.T) {
	prevBudget, prevStatus, prevFacts := branchFactsBudget, worktreeStatusFn, branchFactsFn
	t.Cleanup(func() { branchFactsBudget, worktreeStatusFn, branchFactsFn = prevBudget, prevStatus, prevFacts })
	branchFactsBudget = 0
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	worktreeStatusFn = func(context.Context, string) (gitworktree.StatusReport, error) {
		return gitworktree.StatusReport{Changes: 2, Files: []gitworktree.FileChange{{Code: "??", Path: ".env"}, {Code: " M", Path: "a.go"}}}, nil
	}
	asked := 0
	branchFactsFn = func(context.Context, string, string, string) (gitworktree.BranchFacts, error) {
		asked++
		return gitworktree.BranchFacts{Branch: "x"}, nil
	}
	d := daemonWithRecord(t, wtEntry)

	got := d.agentWorktreeFacts(wtEntry.ID)

	if got.Error != "" || got.Status.Changes != 2 || len(got.Status.Files) != 2 || got.Status.Files[0].Path != ".env" {
		t.Errorf("facts = %+v, want the two files", got)
	}
	if asked != 0 || got.Status.BranchReason != "not checked (out of time)" {
		t.Errorf("branch facts read %d times, reason %q; want none and the out-of-time reason", asked, got.Status.BranchReason)
	}
}
