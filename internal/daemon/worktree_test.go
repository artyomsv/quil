package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

func stubWorktreeList(t *testing.T, out []gitworktree.Worktree, err error) {
	t.Helper()
	prev := worktreeListFn
	worktreeListFn = func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) {
		return out, err
	}
	t.Cleanup(func() { worktreeListFn = prev })
}

// The echo contract: Path comes back byte-identical on EVERY path, because it
// is the client's staleness key. A daemon-side normalisation makes a live
// request look permanently stale.
func TestWorktreeListResponse_EchoesPathVerbatim(t *testing.T) {
	stubWorktreeList(t, nil, errors.New("boom"))
	const raw = "  /repo/./sub/  "
	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: raw}, "/fallback")
	if got.Path != raw {
		t.Errorf("Path = %q, want the request's %q verbatim", got.Path, raw)
	}
}

// "Not a repository" is a real answer and must be distinguishable from a
// failure: only one of the two justifies telling the user there is no
// repository here.
func TestWorktreeListResponse_NotARepoIsNotAnError(t *testing.T) {
	stubWorktreeList(t, nil, nil)
	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/tmp"}, "/fallback")
	if got.Repo {
		t.Error("Repo should be false outside a repository")
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty — not-a-repo is an answer", got.Error)
	}
}

// Root is the MAIN checkout and WorktreeRoot is its sibling worktrees
// directory, both computed daemon-side because they describe the daemon's disk.
func TestWorktreeListResponse_DerivesRootAndWorktreeRoot(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
		{Path: "/projects/quil-worktrees/feat-x", Branch: "feat/x"},
	}, nil)

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil/internal"}, "")
	if !got.Repo {
		t.Fatal("Repo should be true")
	}
	if got.Root != "/projects/quil" {
		t.Errorf("Root = %q, want the main checkout", got.Root)
	}
	if got.WorktreeRoot != "/projects/quil-worktrees" {
		t.Errorf("WorktreeRoot = %q, want the sibling worktrees dir", got.WorktreeRoot)
	}
	if len(got.Worktrees) != 2 {
		t.Fatalf("got %d worktrees, want 2", len(got.Worktrees))
	}
	if !got.Worktrees[0].Main || got.Worktrees[1].Main {
		t.Error("only the first entry is the main checkout")
	}
	if got.Worktrees[1].Branch != "feat/x" {
		t.Errorf("branch = %q, want feat/x", got.Worktrees[1].Branch)
	}
}

// A bare main checkout has no working tree, so there is nothing for a sibling
// path to be a sibling OF. Reported as a repository with no derived root
// rather than a failure — the field renders the reason.
func TestWorktreeListResponse_BareRepoHasNoWorktreeRoot(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil.git", Main: true, Bare: true},
	}, nil)
	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil.git"}, "")
	if !got.Repo {
		t.Fatal("a bare repository is still a repository")
	}
	if got.WorktreeRoot != "" {
		t.Errorf("WorktreeRoot = %q, want empty for a bare repo", got.WorktreeRoot)
	}
	if !got.Worktrees[0].Bare {
		t.Error("the bare flag must survive onto the wire")
	}
}

// An empty Path means the daemon's default directory.
func TestWorktreeListResponse_EmptyPathUsesFallback(t *testing.T) {
	var sawDir string
	prev := worktreeListFn
	worktreeListFn = func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) {
		sawDir = dir
		return nil, nil
	}
	t.Cleanup(func() { worktreeListFn = prev })

	worktreeListResponse(ipc.WorktreeListReqPayload{Path: ""}, "/daemon/cwd")
	if sawDir != "/daemon/cwd" {
		t.Errorf("scanned %q, want the fallback", sawDir)
	}
}

// The worktree slot is independent of the browse and git-discovery slots: the
// setup dialog resolves a directory and then lists its worktrees, so a shared
// guard would fail each step exactly when it followed the other.
func TestBeginWorktreeList_SlotIsIndependent(t *testing.T) {
	d := &Daemon{}
	d.browseScanning.Store(true)
	d.gitDiscovering.Store(true)

	msg, err := ipc.NewMessage(ipc.MsgWorktreeListReq, ipc.WorktreeListReqPayload{Path: "/x"})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if _, ok := d.beginWorktreeList(msg); !ok {
		t.Fatal("claim refused while only the OTHER slots are held")
	}
	// Second claim must be refused, and the rejection must still echo Path.
	rejection, ok := d.beginWorktreeList(msg)
	if ok {
		t.Fatal("second claim should be refused")
	}
	if rejection.Path != "/x" {
		t.Errorf("rejection Path = %q, want /x — a rejection that drops the key is discarded as stale", rejection.Path)
	}
}
