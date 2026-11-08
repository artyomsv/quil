package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// Regression tests for the findings of the whole-branch review of the
// worktree cleanup work. Each one fails against the code as it was reviewed.

// M1: a GONE entry is refused by the daemon, not only by the view: a worktree
// recreated at the same path is listed by git again, and the stale record
// must not remove it.
//
// Fails if the act path does not check Gone.
func TestAgentWTAct_GoneEntryRefused(t *testing.T) {
	gone := wtEntry
	gone.Gone = true
	d := daemonWithRecord(t, gone)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "x"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if !strings.Contains(got.Error, "gone") || len(*calls) != 0 {
		t.Errorf("resp = %+v, removals %v; want a gone refusal and no removal", got, *calls)
	}
}

// M1: the path is listed again, but on ANOTHER branch: it is not the
// worktree the record describes.
//
// Fails if the verification checks the path alone.
func TestAgentWTAct_RecreatedOnAnotherBranchRefused(t *testing.T) {
	d := daemonWithRecord(t, wtEntry)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/x", Branch: "someone-else"}}, nil)
	calls := stubRemoveWorktree(t, nil)

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-wt", RemoveWorktree: true})

	if !strings.Contains(got.Error, "another branch") || len(*calls) != 0 {
		t.Errorf("resp = %+v, removals %v; want a branch refusal and no removal", got, *calls)
	}
}

// M2: the close's branch delete is confirmed against the branch's TIP as the
// dialog read it. A commit made since then (an agent finishing its turn)
// moves the tip, and -D would drop it unseen: refused, with a card.
//
// Fails if the confirmation pins the branch name only.
func TestConfirmRefusal_BranchMovedAfterTheDialog(t *testing.T) {
	opt := branchDeleteOpts{Local: true, Confirmed: confirmations([]ipc.BranchConfirmation{
		{WorktreePath: "/w/feat-a", Branch: "feat/a", Tip: "aaaa"},
	})}
	cases := []struct {
		name string
		f    gitworktree.BranchFacts
		want string
	}{
		{"unchanged", gitworktree.BranchFacts{Branch: "feat/a", Tip: "aaaa"}, ""},
		{"moved", gitworktree.BranchFacts{Branch: "feat/a", Tip: "bbbb"}, "the branch moved after the dialog opened"},
		{"switched", gitworktree.BranchFacts{Branch: "feat/b", Tip: "aaaa"}, "the worktree switched branch after the dialog opened"},
	}
	for _, tc := range cases {
		if got := opt.confirmRefusal("/w/feat-a", tc.f); got != tc.want {
			t.Errorf("%s: confirmRefusal = %q, want %q", tc.name, got, tc.want)
		}
	}
	// An older client sends no tip: a real branch always has one, so nothing
	// it confirms is ever deleted.
	old := branchDeleteOpts{Local: true, Confirmed: confirmations([]ipc.BranchConfirmation{{WorktreePath: "/w/feat-a", Branch: "feat/a"}})}
	if got := old.confirmRefusal("/w/feat-a", gitworktree.BranchFacts{Branch: "feat/a", Tip: "aaaa"}); got == "" {
		t.Error("a confirmation without a tip deleted a branch that has one")
	}
}

// M2, through the close: the branch moved between the dialog and the close.
// The worktree goes, the branch stays, and a card says why.
func TestRemoveWorktreeWithBranch_MovedBranchIsKept(t *testing.T) {
	d := newTestDaemon(t)
	stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/feat-a", Branch: "feat/a"}}, nil)
	stubRemoveWorktree(t, nil)
	prevFacts, prevDef, prevLocal := branchFactsFn, defaultBranchRefFn, deleteLocalBranchFn
	branchFactsFn = func(context.Context, string, string, string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: "feat/a", DefaultRef: "refs/heads/master", Tip: "moved"}, nil
	}
	defaultBranchRefFn = func(context.Context, string) string { return "refs/heads/master" }
	deleted := 0
	deleteLocalBranchFn = func(context.Context, string, string) error { deleted++; return nil }
	t.Cleanup(func() { branchFactsFn, defaultBranchRefFn, deleteLocalBranchFn = prevFacts, prevDef, prevLocal })
	opt := identityOpts()
	opt.Local = true
	opt.Confirmed = confirmations([]ipc.BranchConfirmation{{WorktreePath: "/w/feat-a", Branch: "feat/a", Tip: "shown"}})

	d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, opt)

	if deleted != 0 {
		t.Errorf("deleted %d branches whose tip moved after the dialog", deleted)
	}
	cards := eventsOfType(d, "worktree_branch_kept")
	if len(cards) != 1 || !strings.Contains(cards[0].Message, "moved") {
		t.Errorf("branch-kept cards = %+v, want one saying the branch moved", cards)
	}
}

// M2, through the cleanup view's branch entry: the same tip check.
func TestAgentWTAct_BranchMovedSinceTheViewRefused(t *testing.T) {
	d := daemonWithRecord(t, brEntry)
	prevList, prevFor, prevDef, prevLocal := branchListFn, branchFactsForFn, defaultBranchRefFn, deleteLocalBranchFn
	branchListFn = func(context.Context, string) ([]gitworktree.Branch, bool, error) {
		return []gitworktree.Branch{{Name: "master"}, {Name: "feat/plain"}}, false, nil
	}
	branchFactsForFn = func(_ context.Context, _, b, _ string) (gitworktree.BranchFacts, error) {
		return gitworktree.BranchFacts{Branch: b, DefaultRef: "refs/heads/master", Tip: "moved"}, nil
	}
	defaultBranchRefFn = func(context.Context, string) string { return "refs/heads/master" }
	deleted := 0
	deleteLocalBranchFn = func(context.Context, string, string) error { deleted++; return nil }
	t.Cleanup(func() {
		branchListFn, branchFactsForFn, defaultBranchRefFn, deleteLocalBranchFn = prevList, prevFor, prevDef, prevLocal
	})

	got := d.agentWorktreeAct(ipc.AgentWorktreeActReqPayload{ID: "e-br", DeleteBranch: true,
		Confirmation: ipc.BranchConfirmation{Branch: "feat/plain", Tip: "shown"}})

	if !strings.Contains(got.Error, "moved") || deleted != 0 {
		t.Errorf("resp = %+v, deleted %d; want a moved refusal and no delete", got, deleted)
	}
}

// M3: a baseline LOADED from disk predates the restart. What was made while
// no daemon ran is re-baselined, never recorded as "appeared while <pane>
// ran"; what is made AFTER that first compare is recorded as before; and what
// went while the daemon was down is still marked gone.
//
// Fails if a loaded baseline is compared as if no time had passed.
func TestAgentWT_LoadedBaselineIsRebaselinedOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent-worktrees.json")
	first := newAgentWTStore(file, func(string) (agentWTPane, bool) { return agentWTPane{}, false })
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	first.apply([]gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/old", Branch: "old"}},
		true, []gitworktree.Branch{{Name: "master"}, {Name: "old"}}, "refs/heads/master", agentWTAttr{}, t0)
	first.entries = append(first.entries, AgentWTEntry{ID: "e-old", Kind: agentWTKindWorktree, Repo: "/repo", Path: "/w/old", Branch: "old", FirstSeen: t0})
	first.save()

	s := newAgentWTStore(file, func(string) (agentWTPane, bool) { return agentWTPane{}, false })
	s.load()
	attr := agentWTAttr{PaneID: "pane-1", PaneName: "claude"}
	// While the daemon was down: /w/old removed by hand, /w/manual and branch
	// "manual" made by hand.
	s.apply([]gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/manual", Branch: "manual"}},
		true, []gitworktree.Branch{{Name: "master"}, {Name: "old"}, {Name: "manual"}}, "refs/heads/master", attr, t0.Add(time.Hour))

	for _, e := range s.entries {
		if !e.Gone && (e.Path == "/w/manual" || e.Branch == "manual") {
			t.Fatalf("recorded %+v as made while the pane ran; it was made while no daemon ran", e)
		}
	}
	goneOld := false
	for _, e := range s.entries {
		if e.ID == "e-old" && e.Gone {
			goneOld = true
		}
	}
	if !goneOld {
		t.Error("the worktree removed while the daemon was down is not marked gone")
	}

	// After that first compare, a new worktree IS recorded.
	s.apply([]gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/manual", Branch: "manual"}, {Path: "/w/agent", Branch: "agent"}},
		true, []gitworktree.Branch{{Name: "master"}, {Name: "old"}, {Name: "manual"}, {Name: "agent"}}, "refs/heads/master", attr, t0.Add(2*time.Hour))
	found := false
	for _, e := range s.entries {
		if e.Path == "/w/agent" && !e.Gone {
			found = true
		}
	}
	if !found {
		t.Error("a worktree made after the re-baseline was not recorded")
	}
}

// L2: a record holding a null repository loads without it, instead of
// panicking the first reader.
func TestAgentWT_LoadDropsANullRepository(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent-worktrees.json")
	if err := os.WriteFile(file, []byte(`{"v":1,"repos":{"k":null,"j":{"path":"/repo","worktrees":[]}},"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newAgentWTStore(file, func(string) (agentWTPane, bool) { return agentWTPane{}, false })
	s.load()
	if _, ok := s.repos["k"]; ok {
		t.Error("a null repository record was kept")
	}
	if _, ok := s.repos["j"]; !ok {
		t.Error("a valid repository record was dropped")
	}
	_ = s.uncertainRepos() // panicked on a nil record before
}

// L1: Quil's own worktree is noted BEFORE git runs, so a compare that lands
// during the add never attributes it to another pane; a failed add takes the
// note back.
func TestWorktreeAdd_OwnWorktreeNotedBeforeTheAdd(t *testing.T) {
	d, req, checkout := markTestCreate(t)
	notedDuringAdd := false
	stubAdd(t, func(context.Context, string, string, string) error {
		d.agentWT.mu.Lock()
		notedDuringAdd = d.agentWT.ownPaths[normalizeWorktreePath(checkout)]
		d.agentWT.mu.Unlock()
		return errors.New("git refused")
	})

	d.worktreeAddAndCreate(req)

	if !notedDuringAdd {
		t.Error("the worktree was not noted as Quil's while git was creating it")
	}
	d.agentWT.mu.Lock()
	left := d.agentWT.ownPaths[normalizeWorktreePath(checkout)]
	d.agentWT.mu.Unlock()
	if left {
		t.Error("a failed add left its note behind")
	}
}

// L5: a worktree whose folder was deleted by hand reads "not a repository"
// from git (the listing runs inside it). The kept reason says the folder is
// gone instead.
func TestRemoveOwnedWorktrees_MissingFolderSaysSo(t *testing.T) {
	d := newTestDaemon(t)
	stubWorktreeList(t, nil, nil)
	calls := stubRemoveWorktree(t, nil)
	missing := filepath.Join(t.TempDir(), "deleted-by-hand")
	opt := branchDeleteOpts{Owners: map[string]paneRef{missing: testBranchOwner},
		Tokens: map[string]string{missing: "test-token"}}

	d.removeOwnedWorktreesWith([]string{missing}, opt)

	if len(*calls) != 0 {
		t.Fatalf("removed %v", *calls)
	}
	if c := oneKeptCard(t, d); c.Data["reason"] != keptFolderMissing {
		t.Errorf("card reason = %q, want %q", c.Data["reason"], keptFolderMissing)
	}
}

// L6: a branch absent from a CAPPED listing is no answer: refused as such,
// never as "no longer exists"; a branch present in it still verifies.
func TestVerifyAgentBranch_CappedListingIsNoAnswer(t *testing.T) {
	prev := branchListFn
	branchListFn = func(context.Context, string) ([]gitworktree.Branch, bool, error) {
		return []gitworktree.Branch{{Name: "master"}, {Name: "feat/plain"}}, true, nil
	}
	t.Cleanup(func() { branchListFn = prev })

	if err := verifyAgentBranch(context.Background(), brEntry); err != nil {
		t.Errorf("a branch inside a capped list: %v", err)
	}
	other := brEntry
	other.Branch = "past-the-cap"
	if err := verifyAgentBranch(context.Background(), other); err == nil || !strings.Contains(err.Error(), "in full") {
		t.Errorf("a branch past the cap: err = %v, want a could-not-list refusal", err)
	}
}

// L3: a branch delete git refuses is a card in FIXED words; git's text stays
// in the log.
func TestDeleteBranches_RefusalCardCarriesNoGitText(t *testing.T) {
	d := newTestDaemon(t)
	prev := deleteLocalBranchFn
	deleteLocalBranchFn = func(context.Context, string, string) error {
		return errors.New("error: the branch 'feat/a' is not fully merged at /secret/path")
	}
	t.Cleanup(func() { deleteLocalBranchFn = prev })

	d.deleteBranches("/repo", gitworktree.BranchFacts{Branch: "feat/a"}, branchDeleteOpts{Local: true}, testBranchOwner)

	cards := eventsOfType(d, "worktree_branch_kept")
	if len(cards) != 1 || strings.Contains(cards[0].Message, "/secret/path") || !strings.Contains(cards[0].Message, "quild.log") {
		t.Errorf("cards = %+v, want one fixed refusal", cards)
	}
}
