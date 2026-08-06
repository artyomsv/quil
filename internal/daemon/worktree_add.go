package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// worktreeAddTimeout bounds one `git worktree add`.
//
// Far longer than gitProbeTimeout, deliberately: an add CHECKS OUT A TREE, and
// on a large repository that legitimately takes minutes. Killing it partway
// leaves a half-written worktree git must then prune, so a tight budget trades
// a slow success for a mess the user has to clean up by hand.
//
// Holding a blocking-FS permit that long would normally be the pool-drain
// hazard sweepRoots documents — but the worktreeAdding single-flight IS the
// budget here: at most one add runs daemon-wide, so at most one permit is ever
// held long, however many clients ask and however often.
const worktreeAddTimeout = 120 * time.Second

// addWorktreeFn is the seam tests replace. A package var rather than a Daemon
// field so the handler's OWN claim and validation paths are what run.
var addWorktreeFn = gitworktree.Add

// beginWorktreeAdd claims the worktree-CREATION slot.
//
// Its own atomic, not worktreeScanning: the setup dialog LISTS a directory's
// worktrees and then CREATES one, so a shared guard would reject each step
// exactly when it followed the other — the same reason dirsChecking is not
// browseScanning.
func (d *Daemon) beginWorktreeAdd() bool {
	return d.worktreeAdding.CompareAndSwap(false, true)
}

func (d *Daemon) endWorktreeAdd() { d.worktreeAdding.Store(false) }

// worktreeAddAndCreate creates a linked worktree and spawns the pane inside
// it. Runs on a WORKER goroutine: handleCreatePane runs on the requesting
// connection's dispatch goroutine, where a checkout would block every message
// from that client — input included — for as long as it takes.
//
// Every failure path returns an error and creates NO pane. There is
// deliberately no fallback to the repository root: that spawns an agent on
// master while the user believes it is isolated, which is the exact
// confidently-wrong answer this design exists to remove.
func (d *Daemon) worktreeAddAndCreate(p ipc.CreatePanePayload) ipc.CreatePaneRespPayload {
	// Echoed on EVERY return below, including the error ones. It is the
	// client's staleness key: the client armed a layout placeholder before the
	// send, and nothing but this response will unwind it.
	spec := p.Worktree
	fail := func(format string, args ...any) ipc.CreatePaneRespPayload {
		msg := fmt.Sprintf(format, args...)
		log.Printf("worktree create: %s", msg)
		return ipc.CreatePaneRespPayload{TabID: p.TabID, Error: msg, Worktree: spec}
	}
	if spec == nil {
		return fail("no worktree spec")
	}
	// Refused rather than supported. The client-side replace path sets
	// leaf.Pane = nil and calls old.Dispose() BEFORE the send, so a failed add
	// there costs a LIVE pane — and unlike a dangling placeholder, Dispose() is
	// not something PrunePlaceholders can undo. The dialog omits the field;
	// this is the other half, because any IPC client can send the pair.
	if p.ReplacePaneID != "" {
		return fail("a pane cannot be replaced with one in a new worktree")
	}
	// Validated BEFORE any repository write, so a bad name costs no git
	// invocation, no permit and no slot — and never reaches argv.
	if err := gitworktree.ValidateBranch(spec.Branch); err != nil {
		return fail("%v", err)
	}
	if spec.RepoRoot == "" {
		// Would otherwise reach DerivePath and resolve against the daemon's
		// own working directory, putting a checkout somewhere nobody named.
		return fail("no repository given for the worktree")
	}
	// ABSOLUTE, or the derived path is relative and its two consumers resolve
	// it against different bases: gitworktree.Add runs with cmd.Dir = RepoRoot
	// so git creates the tree under the repository, while the os.Stat in
	// createPaneInWorktree resolves against the DAEMON's working directory.
	// The stat then fails, the create is reported as failed, and the checkout
	// git just made is orphaned on disk with a branch pointing at it.
	if !filepath.IsAbs(spec.RepoRoot) {
		return fail("the repository path must be absolute")
	}
	// Checked BEFORE the expensive work, as well as after it. The post-add
	// re-check exists because the tab can close DURING a checkout; this one
	// exists so a client sending a bogus tab id cannot spend a full checkout
	// (and leave it behind) per request.
	if d.session.Tab(p.TabID) == nil {
		return fail("no such tab")
	}

	if !d.beginWorktreeAdd() {
		return fail("another worktree is being created — try again in a moment")
	}
	defer d.endWorktreeAdd()

	if !claimBlockingFSCall() {
		return fail("the daemon is busy with filesystem work — try again in a moment")
	}
	path := gitworktree.DerivePath(spec.RepoRoot, spec.Branch)
	ctx, cancel := context.WithTimeout(context.Background(), worktreeAddTimeout)
	err := addWorktreeFn(ctx, spec.RepoRoot, path, spec.Branch)
	cancel()
	releaseBlockingFSCall()
	if err != nil {
		// gitworktree.Add attaches git's own stderr — "already used by
		// worktree '/x/feat-y'" names the pane to go look at.
		return fail("%v", err)
	}

	// Re-validated AFTER the add: the window is seconds wide here, not the
	// microseconds an ordinary create has, and a tab can be destroyed inside
	// it. Without this the pane lands in a tab nobody is looking at.
	if d.session.Tab(p.TabID) == nil {
		return fail("the tab was closed while the worktree was being created")
	}

	pane, err := d.createPaneInWorktree(p, path)
	if err != nil {
		return fail("%v", err)
	}
	return ipc.CreatePaneRespPayload{PaneID: pane.ID, TabID: p.TabID, Worktree: spec}
}

// createPaneInWorktree builds the pane with the worktree as its CWD.
//
// It deliberately does NOT reuse handleCreatePane's cwd sanity block. That
// block substitutes d.defaultCWD() whenever the stat fails — a sound
// convention for a browsed directory, where a stale path should not cost the
// user a pane, and exactly the wrong one here, where the directory IS the
// isolation. A worktree path has just been created by this daemon, so its
// absence is a real failure rather than stale user input.
//
// A spawn failure DESTROYS the pane rather than leaving it PTY-less: the
// guarantee this path makes is that a failure produces no pane at all, and a
// half-created one in the tab is exactly what the client's placeholder unwind
// is about to remove.
func (d *Daemon) createPaneInWorktree(p ipc.CreatePanePayload, path string) (*Pane, error) {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("worktree %s is not there after creating it: %v", path, err)
	}
	p.CWD = path
	paneType := p.Type
	if paneType == "" {
		paneType = "terminal"
	}
	pane, err := d.createPaneAt(p, path, paneType)
	if err != nil {
		if pane != nil {
			d.session.DestroyPane(pane.ID)
		}
		return nil, err
	}
	// PluginMu-protected like every other post-publish write: CreatePane has
	// already published the pane, so a snapshot or broadcast goroutine may be
	// reading it.
	pane.PluginMu.Lock()
	pane.WorktreeOwned = true
	pane.PluginMu.Unlock()
	return pane, nil
}
