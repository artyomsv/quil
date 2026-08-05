package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// A response for a directory the user has left is dropped. The path is the
// content key; gen identifies WHICH request, because two requests for the same
// path are otherwise wire-indistinguishable.
func TestApplyWorktreeList_DropsStaleResponses(t *testing.T) {
	m := &Model{}
	m.worktrees = worktreeState{path: "/b", gen: "2", pending: true}

	m.applyWorktreeList(worktreeListMsg{
		Gen:  "1",
		Resp: ipc.WorktreeListRespPayload{Path: "/a", Repo: true},
	})
	if m.worktrees.loaded {
		t.Error("a response for another request must not land")
	}

	m.applyWorktreeList(worktreeListMsg{
		Gen:  "2",
		Resp: ipc.WorktreeListRespPayload{Path: "/b", Repo: true, Worktrees: []ipc.WorktreeInfo{{Path: "/b", Main: true}}},
	})
	if !m.worktrees.loaded || !m.worktrees.repo {
		t.Error("the matching response must land")
	}
	if m.worktrees.pending {
		t.Error("pending must clear on a matching response")
	}
}

// A timeout may only clear the request it belongs to. Without the gen check a
// previous request's late tick wipes the listing the current one just
// delivered.
func TestApplyWorktreeTimeout_OnlyFiresForItsOwnRequest(t *testing.T) {
	m := &Model{}
	m.worktrees = worktreeState{path: "/b", gen: "2", pending: true}

	m.applyWorktreeTimeout(worktreeTimeoutMsg{path: "/b", gen: "1"})
	if !m.worktrees.pending {
		t.Error("a stale timeout must not clear a live request")
	}

	m.applyWorktreeTimeout(worktreeTimeoutMsg{path: "/b", gen: "2"})
	if m.worktrees.pending {
		t.Error("the matching timeout must clear pending")
	}
	if m.worktrees.err == "" {
		t.Error("a timeout must record a reason, or the field says 'no worktrees' for a scan that never answered")
	}
}

// "Not a repository" and "the scan failed" are different facts and only one of
// them may be rendered as an absence of worktrees.
func TestApplyWorktreeList_NotARepoIsNotAnError(t *testing.T) {
	m := &Model{}
	m.worktrees = worktreeState{path: "/tmp", gen: "1", pending: true}
	m.applyWorktreeList(worktreeListMsg{
		Gen:  "1",
		Resp: ipc.WorktreeListRespPayload{Path: "/tmp"},
	})
	if m.worktrees.err != "" {
		t.Errorf("err = %q, want empty", m.worktrees.err)
	}
	if m.worktrees.repo {
		t.Error("repo should be false")
	}
	if !m.worktrees.loaded {
		t.Error("the answer still counts as loaded — the field must stop saying 'scanning'")
	}
}
