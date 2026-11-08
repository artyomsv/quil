package daemon

import (
	"context"
	"encoding/json"
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
	deleteLocalBranchFn = func(context.Context, string, string, string) error { deleted++; return nil }
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
	deleteLocalBranchFn = func(context.Context, string, string, string) error { deleted++; return nil }
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
		true, []gitworktree.Branch{{Name: "master"}, {Name: "old"}, {Name: "gone-br"}}, "refs/heads/master", agentWTAttr{}, t0)
	first.entries = append(first.entries, AgentWTEntry{ID: "e-old", Kind: agentWTKindWorktree, Repo: "/repo", Path: "/w/old", Branch: "old", FirstSeen: t0})
	first.entries = append(first.entries, AgentWTEntry{ID: "e-br-gone", Kind: agentWTKindBranch, Repo: "/repo", Branch: "gone-br", FirstSeen: t0})
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
	goneBr := false
	for _, e := range s.entries {
		if e.ID == "e-br-gone" && e.Gone {
			goneBr = true
		}
	}
	if !goneBr {
		t.Error("the branch deleted while the daemon was down is not marked gone")
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
	d.agentWT = newAgentWTStore("", func(string) (agentWTPane, bool) { return agentWTPane{}, false })
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
	deleteLocalBranchFn = func(context.Context, string, string, string) error {
		return errors.New("error: the branch 'feat/a' is not fully merged at /secret/path")
	}
	t.Cleanup(func() { deleteLocalBranchFn = prev })

	d.deleteBranches("/repo", gitworktree.BranchFacts{Branch: "feat/a"}, branchDeleteOpts{Local: true}, testBranchOwner)

	cards := eventsOfType(d, "worktree_branch_kept")
	if len(cards) != 1 || strings.Contains(cards[0].Message, "/secret/path") || !strings.Contains(cards[0].Message, "quild.log") {
		t.Errorf("cards = %+v, want one fixed refusal", cards)
	}
}

// H-11: the branch name and tip a request carries are PUBLIC (any client can
// read them from agent_worktree_facts_req), so they are no consent. A
// worktree removal or a branch delete is refused from anything but a Quil
// window, through the real socket; the unmerged branch survives.
//
// Fails if a cooperating non-TUI client can delete a branch with the facts it
// read itself.
func TestAgentWTActReq_DestructiveActNeedsAQuilWindow(t *testing.T) {
	d, client := mcpTestDaemon(t) // says hello as an MCP bridge
	s, _ := newTestAgentWT(t, &fakeRepo{}, nil)
	s.paneFn = d.paneForAgentWT
	s.entries = append(s.entries, brEntry, wtEntry)
	d.agentWT = s
	deleted := 0
	prev := deleteLocalBranchFn
	deleteLocalBranchFn = func(context.Context, string, string, string) error { deleted++; return nil }
	t.Cleanup(func() { deleteLocalBranchFn = prev })

	for _, req := range []ipc.AgentWorktreeActReqPayload{
		{ID: "e-br", DeleteBranch: true, Confirmation: ipc.BranchConfirmation{Branch: "feat/plain", Tip: "public"}},
		{ID: "e-wt", RemoveWorktree: true, DeleteBranch: true, Confirmation: ipc.BranchConfirmation{Branch: "x", Tip: "public"}},
		{ID: "e-wt", RemoveWorktree: true},
	} {
		resp := roundTrip(t, client, ipc.MsgAgentWorktreeActReq, ipc.MsgAgentWorktreeActResp, req)
		var out ipc.AgentWorktreeActRespPayload
		if err := json.Unmarshal(resp.Payload, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.Error, "only a Quil window") {
			t.Errorf("request %+v answered %+v, want a refusal", req, out)
		}
	}
	if deleted != 0 {
		t.Errorf("deleted %d branches for a non-TUI client", deleted)
	}
}

// Forget changes no git state and stays open to any client; a Quil window
// may remove and delete.
func TestDestructiveActRefusal_Roles(t *testing.T) {
	for _, role := range []string{"bridge", "web", ""} {
		for _, req := range []ipc.AgentWorktreeActReqPayload{{RemoveWorktree: true}, {DeleteBranch: true}} {
			if destructiveActRefusal(req, role) == "" {
				t.Errorf("role %q may %+v", role, req)
			}
		}
		if r := destructiveActRefusal(ipc.AgentWorktreeActReqPayload{Forget: true}, role); r != "" {
			t.Errorf("role %q refused a forget: %s", role, r)
		}
	}
	if r := destructiveActRefusal(ipc.AgentWorktreeActReqPayload{RemoveWorktree: true, DeleteBranch: true}, "tui"); r != "" {
		t.Errorf("a Quil window was refused: %s", r)
	}
}

// The close path honours a branch delete only from a Quil window; an
// in-process caller (no conn) keeps the flag.
func TestBranchDeleteAllowed_OnlyFromAQuilWindow(t *testing.T) {
	d := newTestDaemon(t)
	if d.branchDeleteAllowed(bridgeConn(d), true) {
		t.Error("an MCP bridge's close deleted a branch")
	}
	if !d.branchDeleteAllowed(tuiConn(d), true) {
		t.Error("a Quil window's close may not delete a branch")
	}
	if !d.branchDeleteAllowed(nil, true) || d.branchDeleteAllowed(tuiConn(d), false) {
		t.Error("the flag is not passed through for an in-process caller or an unticked row")
	}
}

// H-12: a close that did not come from a Quil window (nobody saw the file
// list) removes a worktree only while git status counts nothing in it, and
// never forced; a checkout holding changes is kept with a card.
//
// Fails if an unattended close reaches the forced removal, or removes a
// checkout that holds anything.
func TestRemoveOwnedWorktrees_UnattendedRemovalNeedsACleanCheckout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes int
		wantRm  bool
	}{
		{"holds changes", 2, false},
		{"clean", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDaemon(t)
			stubWorktreeList(t, []gitworktree.Worktree{{Path: "/repo", Main: true}, {Path: "/w/feat-a", Branch: "x"}}, nil)
			forced := stubRemoveWorktree(t, nil)
			stubWorktreeStatus(t, func(string) (int, error) { return tc.changes, nil })
			var clean []string
			prev := removeCleanWorktreeFn
			removeCleanWorktreeFn = func(_ context.Context, _, path string) error { clean = append(clean, path); return nil }
			t.Cleanup(func() { removeCleanWorktreeFn = prev })
			opt := identityOpts()
			opt.Unattended = true

			d.removeOwnedWorktreesWith([]string{"/w/feat-a"}, opt)

			if len(*forced) != 0 {
				t.Errorf("an unattended close used the forced removal: %v", *forced)
			}
			if got := len(clean) == 1; got != tc.wantRm {
				t.Errorf("unforced removals = %v, want removed=%v", clean, tc.wantRm)
			}
			if !tc.wantRm {
				if c := oneKeptCard(t, d); c.Data["reason"] != keptHasChanges {
					t.Errorf("card reason = %q, want %q", c.Data["reason"], keptHasChanges)
				}
			}
		})
	}
}

// Only a close from a Quil window is attended; an in-process caller (no
// conn) is too.
func TestCloseUnattended_Roles(t *testing.T) {
	d := newTestDaemon(t)
	if !d.closeUnattended(bridgeConn(d)) {
		t.Error("an MCP bridge's close counts as confirmed in a Quil window")
	}
	if d.closeUnattended(tuiConn(d)) || d.closeUnattended(nil) {
		t.Error("a Quil window's close, or an in-process one, counts as unattended")
	}
}

// H-12 end to end: a non-TUI client (the socket says hello as an MCP bridge)
// sends destroy_pane_req{remove_worktree} for a pane in a REAL Quil-made,
// Quil-marked worktree that holds a tracked change, an untracked file or an
// ignored one. The pane closes; the worktree and the file survive.
func TestDestroyPaneReq_RealGit_UnattendedRemovalKeepsWork(t *testing.T) {
	for _, tc := range []struct{ name, file, content string }{
		{"tracked change", "f.txt", "changed\n"},
		{"untracked", "new.txt", "new\n"},
		{"ignored", "local.env", "SECRET=1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := realGitRepoForDaemon(t)
			if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.env\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitForAbandon(t, repo, "add", ".gitignore")
			runGitForAbandon(t, repo, "commit", "-q", "-m", "ignore env")
			t.Setenv("QUIL_HOME", t.TempDir())
			d, client := mcpTestDaemon(t)
			tabID, _ := seedTab(t, d, client)
			waitCreateWorkersAtCleanup(t, d)
			created := d.worktreeAddAndCreate(worktreeCreate(tabID, repo, "feat/keep"))
			if created.Error != "" || created.PaneID == "" {
				t.Fatalf("create: %+v", created)
			}
			path := gitworktree.DerivePath(repo, "feat/keep")
			if err := os.WriteFile(filepath.Join(path, tc.file), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}

			roundTrip(t, client, ipc.MsgDestroyPaneReq, ipc.MsgDestroyPaneResp,
				ipc.DestroyPaneReqPayload{PaneID: created.PaneID, RemoveWorktree: true})

			deadline := time.Now().Add(15 * time.Second)
			for len(eventsOfType(d, "worktree_kept")) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("no kept card: the unattended removal did not decide")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if c := eventsOfType(d, "worktree_kept")[0]; c.Data["reason"] != keptHasChanges {
				t.Errorf("card reason = %q, want %q", c.Data["reason"], keptHasChanges)
			}
			if _, err := os.Stat(filepath.Join(path, tc.file)); err != nil {
				t.Errorf("the %s file is gone: %v", tc.name, err)
			}
		})
	}
}
