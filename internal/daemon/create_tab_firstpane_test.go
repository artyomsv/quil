package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// A new tab used to come up with a hardcoded `terminal` pane whatever the user
// wanted, so a plugin pane cost a create-then-replace every single time. The
// client now names the first pane on the create message itself, which is what
// keeps the tab and its pane one atomic step — see handleCreateTab.

// createTabMsg builds the real wire message, so a payload field that never
// reaches the daemon fails here rather than in a hand-built struct the handler
// would never see.
func createTabMsg(t *testing.T, spec *ipc.FirstPaneSpec) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{
		Name:      "New Tab",
		FirstPane: spec,
	})
	if err != nil {
		t.Fatalf("build create_tab message: %v", err)
	}
	return msg
}

// sendCreateTab drives the REAL dispatch — handleMessage → handleCreateTab with
// a live conn — which is the only way the worktree leg's response path runs at
// all. A direct handler call would hand it a nil conn, and passing that would
// require a guard in production code written for no reason but this test.
func sendCreateTab(t *testing.T, c *ipc.Client, spec *ipc.FirstPaneSpec) {
	t.Helper()
	if err := c.Send(createTabMsg(t, spec)); err != nil {
		t.Fatalf("send create_tab: %v", err)
	}
}

// newTabPane returns the sole pane of the tab handleCreateTab just made. It
// resolves through ActiveTabID because the handler switches to the tab it
// creates, which is the same fact the client relies on.
func newTabPane(t *testing.T, d *Daemon) *Pane {
	t.Helper()
	panes := d.session.Panes(d.session.ActiveTabID())
	if len(panes) != 1 {
		t.Fatalf("new tab has %d panes, want exactly 1", len(panes))
	}
	return panes[0]
}

// paneType reads Type under the lock that protects it. CreatePane PUBLISHES the
// pane into the session maps before the type is settled and before the PTY is
// installed, so a bare field read from the test goroutine races the handler
// that is still finishing — which the race detector reports against
// handleAttach's own `pane.Type = "terminal"` for the default workspace.
func paneType(p *Pane) string {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.Type
}

// paneSpawned reports that the handler is DONE with this pane: constructPaneAt
// publishes it into the session maps and only then reads newSessionFn and
// spawns. Waiting on mere existence therefore returns while the conn goroutine
// is still running, and the package-level newSessionFn it is about to read is
// restored by this test's own cleanup — a race that lands on whichever test
// runs next. Every wait in this file settles on THIS rather than on presence.
func paneSpawned(p *Pane) bool {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.PTY != nil || p.SpawnError != ""
}

// settled waits until the daemon has finished everything the test asked for:
// wantTabs tabs exist and EVERY pane in them has spawned.
//
// Both halves were learnt the hard way, and both are about waiting for state
// that is published before the work is done.
//
// wantTabs makes this wait for the CREATE rather than for the attach.
// attachTestClient bootstraps a tab of its own and spawns its pane, so a wait
// that only asks "is everything spawned" is satisfied before create_tab has
// been handled at all: the assertions then read the ATTACH's tab — which holds
// a terminal, so the placeholder test passed without ever seeing a placeholder
// — and the deferred join went on to wait for a worker whose stub cleanup had
// already restored, i.e. the real gitworktree.Add ran instead.
//
// EVERY tab rather than the active one, because these tests attach WITHOUT
// pre-creating a tab: handleAttach's bootstrapped pane sits in the tab that
// create_tab then switches AWAY from, and leaving that spawn in flight let a
// conn goroutine read newSessionFn after this test's cleanup had restored it.
// (The package's other server-driven tests never hit either problem because
// they create their tab up front, so attach bootstraps nothing.)
func settled(t *testing.T, d *Daemon, wantTabs int) {
	t.Helper()
	waitUntil(t, "every pane to finish spawning", func() bool {
		tabs := d.session.Tabs()
		if len(tabs) < wantTabs {
			return false
		}
		for _, tab := range tabs {
			panes := d.session.Panes(tab.ID)
			if len(panes) == 0 {
				return false
			}
			for _, p := range panes {
				if !paneSpawned(p) {
					return false
				}
			}
		}
		return true
	})
}

// sameDir compares through EvalSymlinks because the daemon resolves the
// directory before it spawns, and t.TempDir() hands back a path that is a
// symlink on macOS (/tmp → /private/tmp). A raw string compare is green on
// Linux CI and red on a developer's laptop for a reason that has nothing to do
// with the code under test.
func sameDir(t *testing.T, got, want string) bool {
	t.Helper()
	if got == "" {
		return false
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve %q: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolve %q: %v", want, err)
	}
	return gotResolved == wantResolved
}

func TestHandleCreateTab_SpawnsTheRequestedFirstPaneType(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{Type: "terminal-wide"}))

	if got := paneType(newTabPane(t, d)); got != "terminal-wide" {
		t.Errorf("first pane type = %q, want the requested %q", got, "terminal-wide")
	}
}

// The nil case is every OTHER producer of this message — an older client, and
// any future one that has no opinion. It must keep landing on a shell, because
// the recovery paths that share this shape (attach bootstrap, recoverEmptyProject,
// ensureTabNotEmpty) can never block on a prompt.
func TestHandleCreateTab_NoFirstPaneKeepsTheTerminalDefault(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())

	d.handleCreateTab(nil, createTabMsg(t, nil))

	if got := paneType(newTabPane(t, d)); got != "terminal" {
		t.Errorf("first pane type = %q, want the terminal default", got)
	}
}

// A first pane that names no directory must land where the tab would have
// landed WITHOUT this feature: the owning project's root.
//
// This is the regression the obvious implementation causes. Routing the spec
// through handleCreatePane's body reuses its CWD fallback, which is
// defaultCWD() — the attached client's directory, not the project's. Every
// submit that picks no directory (a plain terminal, any plugin without
// prompts_cwd) would then silently stop opening in the project root, breaking a
// different feature than the one being built.
func TestHandleCreateTab_FirstPaneWithoutCWDUsesTheProjectDirectory(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	root := t.TempDir()
	proj := d.session.CreateProject("alpha", root)
	d.session.SwitchProject(proj.ID)

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{Type: "terminal-wide"}))

	if got := paneCWD(newTabPane(t, d)); !sameDir(t, got, root) {
		t.Errorf("first pane CWD = %q, want the project root %q", got, root)
	}
}

// A tab and its first pane are ONE change and must reach clients as one frame.
//
// The obvious implementation routes the spec through createPaneAt, which
// broadcasts and requests a snapshot itself (daemon.go) — so every new tab would
// put two full workspace-state frames back to back onto a 64-slot must-deliver
// queue, the 2026-08-09 force-disconnect shape, with each frame also driving a
// full applyWorkspaceState reconciliation on every attached client.
//
// Verified by mutation while writing it: swapping constructPaneAt for
// createPaneAt in handleCreateTab makes this report 2.
func TestHandleCreateTab_FirstPaneBroadcastsExactlyOnce(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())

	client := attachTestClient(t, sock)
	defer client.Close()
	frames := countWorkspaceFrames(client)
	waitUntil(t, "the attach broadcast to land", func() bool { return frames.Count() > 0 })
	frames.Reset()

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{Type: "terminal-wide"}))

	// The spawn and anything it wakes are asynchronous; give a second frame
	// time to arrive rather than racing it into a pass.
	time.Sleep(300 * time.Millisecond)

	if n := frames.Count(); n != 1 {
		t.Errorf("creating a tab with a first pane broadcast %d workspace_state frames, want exactly 1", n)
	}
}

// A worktree first pane opens as a TERMINAL and is replaced once git has
// finished — it does NOT open as the requested type in the meantime.
//
// `git worktree add` checks out a tree: seconds on a large repository, up to
// worktreeAddTimeout. Something has to occupy the tab for that window, and the
// requested type is the one thing it must not be. The whole point of a worktree
// pane is that the agent is isolated from the main checkout, so spawning
// claude-code in the project root "just for a moment" starts an agent in
// exactly the tree the feature exists to keep it out of — isolated in its
// directory and not in its history, which is invisible until somebody reads the
// diff days later. A shell there is harmless.
// Driven by SENDING the message, not by calling the handler: the worktree leg
// answers the requester, so it needs a real conn, and a handler call with a nil
// one would only pass if production code carried a guard written for the test.
func TestHandleCreateTab_WorktreeFirstPaneOpensAsATerminal(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	release := make(chan struct{})
	done := joinableAdd(t, func(context.Context, string, string, string) error {
		<-release // hold the checkout open so the placeholder is observable
		return errors.New("released")
	})
	// Released and JOINED inside the test body, never left to cleanup: the
	// worker is executing addWorktreeFn while stubAdd's own cleanup restores
	// that package var, so releasing it there races the restore — and the
	// leftover goroutine then reads whatever the NEXT test installed.
	defer func() {
		close(release)
		joinAdd(t, done)
	}()

	client := attachTestClient(t, sock)
	defer client.Close()
	sendCreateTab(t, client, &ipc.FirstPaneSpec{
		Type:     "terminal-wide",
		Worktree: &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: "feat/x"},
	})

	settled(t, d, 2)

	if got := paneType(newTabPane(t, d)); got != "terminal" {
		t.Errorf("placeholder pane type = %q, want a plain terminal while the checkout runs", got)
	}
}

// The placeholder must carry NONE of the requested plugin's fields, not just a
// different Type.
//
// resolveSpawnArgs REPLACES a plugin's own args whenever the pane has any
// InstanceArgs, so a placeholder that inherits them is spawned as
// `<shell> <args meant for a different program>`. shellinit.Configure hides it
// on bash/zsh/pwsh by overwriting args — and returns nil for sh, dash, fish and
// anything unrecognised, where the shell dies on its first instruction and the
// "a failure leaves a harmless terminal" guarantee stops being true. Reachable
// without an adversary: Ctrl+T → claude-code → permission toggle → new branch.
//
// ResumeSessionID rides along for the same reason and must go too: it writes a
// resume claim onto a TERMINAL pane, which any snapshot inside the checkout
// window persists, and which outlives a failed add.
func TestHandleCreateTab_WorktreePlaceholderCarriesNoPluginFields(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	release := make(chan struct{})
	done := joinableAdd(t, func(context.Context, string, string, string) error {
		<-release
		return errors.New("released")
	})
	defer func() {
		close(release)
		joinAdd(t, done)
	}()

	client := attachTestClient(t, sock)
	defer client.Close()
	sendCreateTab(t, client, &ipc.FirstPaneSpec{
		Type:            "terminal-wide",
		InstanceName:    "inst",
		InstanceArgs:    []string{"--dangerously-skip-permissions"},
		ResumeSessionID: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		Worktree:        &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: "feat/x"},
	})

	settled(t, d, 2)

	pane := newTabPane(t, d)
	pane.PluginMu.Lock()
	args, name := pane.InstanceArgs, pane.InstanceName
	resume := pane.PluginState["resume_session_id"]
	pane.PluginMu.Unlock()

	if len(args) != 0 {
		t.Errorf("placeholder InstanceArgs = %v, want none — these replace the shell's own args", args)
	}
	if name != "" {
		t.Errorf("placeholder InstanceName = %q, want empty", name)
	}
	if resume != "" {
		t.Errorf("placeholder holds resume_session_id %q, want none on a terminal", resume)
	}
}

// And the requested pane really does arrive, in the worktree, replacing that
// placeholder — driven through the real handler over a real conn, because the
// leg runs on a worker goroutine and answers the requester.
func TestHandleCreateTab_WorktreeSwapsInTheRequestedPane(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	repo := t.TempDir()
	// Derived, not captured from the stub: the stub runs on the daemon's worker
	// goroutine, so a variable it assigns and this goroutine reads is a data
	// race — and DerivePath is what the daemon itself uses, so asserting
	// against it also pins that the pane lands where the daemon said it would.
	wantPath := gitworktree.DerivePath(repo, "feat/x")
	done := joinableAdd(t, func(_ context.Context, _, path, _ string) error {
		// The real git makes the directory; createPaneInWorktree stats it and
		// fails the create if it is missing, so a stub that only records is
		// indistinguishable from a broken add.
		return os.MkdirAll(path, 0o755)
	})
	defer func() { joinAdd(t, done) }()

	client := attachTestClient(t, sock)
	defer client.Close()
	sendCreateTab(t, client, &ipc.FirstPaneSpec{
		Type:     "terminal-wide",
		Worktree: &ipc.WorktreeSpec{RepoRoot: repo, Branch: "feat/x"},
	})

	// Waits on WorktreeOwned, which createPaneInWorktree sets LAST — after
	// replacePaneAt has already published the pane. Waiting on the type alone
	// is satisfied one step early, so the ownership assertion below read a flag
	// that had not been written yet (~9 failures in 12 runs).
	waitUntil(t, "the worktree pane to replace the placeholder", func() bool {
		panes := d.session.Panes(d.session.ActiveTabID())
		return len(panes) == 1 && paneType(panes[0]) == "terminal-wide" &&
			worktreeOwned(panes[0]) && paneSpawned(panes[0])
	})
	settled(t, d, 2)

	pane := d.session.Panes(d.session.ActiveTabID())[0]
	if got := paneCWD(pane); !sameDir(t, got, wantPath) {
		t.Errorf("pane CWD = %q, want the worktree %q", got, wantPath)
	}
}

// paneCWD reads CWD under PluginMu — it is on the same protected set as Type
// (the lazy-spawn path rewrites both on its error branches while IPC readers
// are live).
func paneCWD(p *Pane) string {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.CWD
}

// worktreeOwned reads the flag under the lock every other post-publish write to
// a live pane takes — the pane is in the session maps by now, so the snapshot
// and broadcast goroutines are legitimate concurrent readers.
func worktreeOwned(p *Pane) bool {
	p.PluginMu.Lock()
	defer p.PluginMu.Unlock()
	return p.WorktreeOwned
}

// joinableAdd is stubAdd plus a channel closed when the add returns.
//
// createFirstPaneWorktree launches its worker AFTER the placeholder pane is
// spawned, so every "the tab has its pane" wait can be satisfied before that
// goroutine has even started — and it reads addWorktreeFn on its first line,
// which stubAdd's cleanup is by then restoring. Joining is the only ordering
// that holds; no amount of waiting on daemon-visible state does, because the
// state the worker is about to read is not daemon state.
func joinableAdd(t *testing.T, fn func(ctx context.Context, repo, path, branch string) error) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	stubAdd(t, func(ctx context.Context, repo, path, branch string) error {
		defer close(done)
		return fn(ctx, repo, path, branch)
	})
	return done
}

// joinAdd waits for the daemon's worktree worker, bounded so a wiring mistake
// fails the test rather than hanging the package.
func joinAdd(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the worktree worker to finish")
	}
}

// A failed add leaves the tab with the terminal it already had, and never the
// requested pane: the user asked for isolation, so relocating the agent to the
// repository root is the confidently-wrong answer worktreeAddAndCreate exists
// to refuse. The tab is NOT left empty either — there is nothing to unwind,
// because the placeholder was never detached.
func TestHandleCreateTab_WorktreeFailureKeepsTheTerminal(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	done := joinableAdd(t, func(context.Context, string, string, string) error {
		return errors.New("fatal: 'feat/x' is already used by worktree at '/x/feat-y'")
	})

	client := attachTestClient(t, sock)
	defer client.Close()
	sendCreateTab(t, client, &ipc.FirstPaneSpec{
		Type:     "terminal-wide",
		Worktree: &ipc.WorktreeSpec{RepoRoot: t.TempDir(), Branch: "feat/x"},
	})

	settled(t, d, 2)
	// Join the worker rather than sleeping past it: the assertion is about what
	// the FAILURE left behind, so it has to run after the failure was handled.
	joinAdd(t, done)
	time.Sleep(100 * time.Millisecond)

	panes := d.session.Panes(d.session.ActiveTabID())
	if len(panes) != 1 {
		t.Fatalf("tab has %d panes after a failed add, want the untouched placeholder", len(panes))
	}
	if got := paneType(panes[0]); got != "terminal" {
		t.Errorf("pane type = %q, want the terminal left in place", got)
	}
}

// A client-supplied directory that never answers must not park the dispatch
// goroutine.
//
// Before this feature a new tab's directory came ONLY from projectCWD, which
// routes through resolveSpawnDirWithin — permit-claimed and bounded. Accepting
// spec.CWD off the wire and stat-ing it inline gave create_tab back the
// unbounded syscall that primitive's own doc comment says it was written to
// remove: a path on a dead NFS/SMB mount parks the requesting connection's
// goroutine, and with it every later message from that client, input included.
//
// Driven through the statPath seam because no real filesystem provides a path
// that never answers on demand.
// The call count is the load-bearing assertion, not the elapsed time: an inline
// os.Stat on a path that does not exist returns instantly, so a timing-only test
// stays green against exactly the code this is meant to refuse. statPath being
// reached is what proves the resolution went through the BOUNDED primitive.
func TestResolveRequestedCWD_HungPathFallsBackWithinTheBudget(t *testing.T) {
	block := make(chan struct{})
	var calls atomic.Int64
	orig := statPath
	statPath = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-block
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { statPath = orig }) })

	d := overlayTestDaemon(t, config.Default())
	fallback := t.TempDir()

	done := make(chan string, 1)
	go func() { done <- d.resolveRequestedCWD("/mnt/dead", fallback) }()

	select {
	case got := <-done:
		if got != fallback {
			t.Errorf("resolveRequestedCWD = %q, want the fallback %q", got, fallback)
		}
	case <-time.After(spawnDirProbeTimeout + 3*time.Second):
		t.Fatal("resolveRequestedCWD never returned — a dead mount parks the dispatch goroutine")
	}
	if calls.Load() == 0 {
		t.Error("statPath was never reached — the client-supplied directory is being stat-ed " +
			"inline on the dispatch goroutine, with no budget and no permit")
	}
}

// An explicit directory still wins — that is the whole point of the setup
// dialog's CWD field.
func TestHandleCreateTab_FirstPaneHonorsAnExplicitCWD(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	proj := d.session.CreateProject("alpha", t.TempDir())
	d.session.SwitchProject(proj.ID)
	chosen := t.TempDir()

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{Type: "terminal-wide", CWD: chosen}))

	if got := paneCWD(newTabPane(t, d)); !sameDir(t, got, chosen) {
		t.Errorf("first pane CWD = %q, want the chosen directory %q", got, chosen)
	}
}
