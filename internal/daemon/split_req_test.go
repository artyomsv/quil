package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/layouttree"
	apty "github.com/artyomsv/quil/internal/pty"
)

func split(t *testing.T, client *ipc.Client, req ipc.SplitPaneReqPayload) ipc.SplitPaneRespPayload {
	t.Helper()
	return decodeInto[ipc.SplitPaneRespPayload](t, roundTrip(t, client, ipc.MsgSplitPaneReq, ipc.MsgSplitPaneResp, req))
}

// tabTree reads a tab's stored tree under the session lock.
func tabTree(t *testing.T, d *Daemon, tabID string) (*layouttree.Node, uint64) {
	t.Helper()
	_, tabs, _, _, _ := d.session.SnapshotState()
	for _, tab := range tabs {
		if tab.ID == tabID {
			n, _ := layouttree.Parse(tab.Layout)
			return n, tab.LayoutRev
		}
	}
	t.Fatalf("tab %s not in the snapshot", tabID)
	return nil, 0
}

// seedTab makes a tab with one terminal pane and returns both ids.
func seedTab(t *testing.T, d *Daemon, client *ipc.Client) (tabID, paneID string) {
	t.Helper()
	tab := d.session.CreateTab("t")
	resp := split(t, client, ipc.SplitPaneReqPayload{TabID: tab.ID, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}})
	if resp.Error != "" || resp.PaneID == "" {
		t.Fatalf("seed: %+v", resp)
	}
	return tab.ID, resp.PaneID
}

// countOverlays counts the overlay panes in a tab.
func countOverlays(d *Daemon, tabID string) int {
	n := 0
	for _, p := range d.session.Panes(tabID) {
		p.PluginMu.Lock()
		if p.Overlay {
			n++
		}
		p.PluginMu.Unlock()
	}
	return n
}

func TestSplitPaneReq_RightAndBelowPlaceThePaneNextToTheTarget(t *testing.T) {
	for _, tc := range []struct {
		placement string
		want      layouttree.SplitDir
	}{{ipc.PlacementRight, layouttree.Horizontal}, {ipc.PlacementBelow, layouttree.Vertical}} {
		t.Run(tc.placement, func(t *testing.T) {
			d, client := mcpTestDaemon(t)
			tabID, first := seedTab(t, d, client)
			_, revBefore := tabTree(t, d, tabID)

			resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: tc.placement, Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}})
			if resp.Error != "" || resp.PaneID == "" || resp.TabID != tabID {
				t.Fatalf("resp = %+v", resp)
			}
			tree, rev := tabTree(t, d, tabID)
			if rev <= revBefore || resp.LayoutRev != rev {
				t.Fatalf("layout_rev: answer %d, stored %d, before %d", resp.LayoutRev, rev, revBefore)
			}
			if tree == nil || tree.Split == nil || *tree.Split != tc.want {
				t.Fatalf("root = %+v, want a split of direction %d", tree, tc.want)
			}
			if tree.Left.PaneID != first || tree.Right.PaneID != resp.PaneID {
				t.Fatalf("leaves %q | %q, want %q | %q", tree.Left.PaneID, tree.Right.PaneID, first, resp.PaneID)
			}
		})
	}
}

func TestSplitPaneReq_ReplaceKeepsTheSlot(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	second := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementBelow, Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}}).PaneID
	before, _ := tabTree(t, d, tabID)

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: second, Placement: ipc.PlacementReplace, Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}})
	if resp.Error != "" || resp.PaneID == "" {
		t.Fatalf("resp = %+v", resp)
	}
	if d.session.Pane(second) != nil {
		t.Fatal("the replaced pane is still in the session")
	}
	after, _ := tabTree(t, d, tabID)
	if *after.Split != *before.Split || after.Ratio != before.Ratio || after.Left.PaneID != first || after.Right.PaneID != resp.PaneID {
		t.Fatalf("tree after replace = %+v, want the old slot with the new id", after)
	}
}

func TestSplitPaneReq_Refusals(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	overlay := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementOverlay, OverlayKind: "lazygit", Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}})
	if overlay.Error != "" {
		t.Fatalf("overlay: %+v", overlay)
	}
	placeholder, err := d.constructPreparingPane(tabID, t.TempDir(), "terminal", ipc.FirstPaneSpec{Worktree: &ipc.WorktreeSpec{Branch: "feat/x"}})
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.buildPaneInfos())
	for name, tc := range map[string]struct {
		req  ipc.SplitPaneReqPayload
		want string
	}{
		"unknown target":            {ipc.SplitPaneReqPayload{TargetPaneID: "pane-00000000", Placement: ipc.PlacementRight}, "no such pane"},
		"overlay target":            {ipc.SplitPaneReqPayload{TargetPaneID: overlay.PaneID, Placement: ipc.PlacementRight}, "overlay"},
		"replace a placeholder":     {ipc.SplitPaneReqPayload{TargetPaneID: placeholder.ID, Placement: ipc.PlacementReplace}, "preparing"},
		"unknown placement":         {ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: "diagonal"}, "placement"},
		"unknown overlay kind":      {ipc.SplitPaneReqPayload{TabID: tabID, Placement: ipc.PlacementOverlay, OverlayKind: "vim"}, "overlay kind"},
		"unknown tab":               {ipc.SplitPaneReqPayload{TabID: "tab-nope", Placement: ipc.PlacementRight}, "no such tab"},
		"unknown toggle":            {ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Type: "claude-code", Toggles: []string{"nope"}}}, "unknown toggle"},
		"bad sandbox image":         {ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Type: "claude-code", Sandbox: &ipc.SandboxSpec{Image: "--privileged"}}}, ""},
		"bad resume id":             {ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Type: "claude-code", ResumeSessionID: "not-a-uuid"}}, "resume"},
		"missing existing worktree": {ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Worktree: &ipc.SplitWorktree{ExistingPath: filepath.Join(t.TempDir(), "gone")}}}, "worktree"},
		"both worktree kinds":       {ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Worktree: &ipc.SplitWorktree{Branch: "a", ExistingPath: t.TempDir()}}}, "worktree"},
	} {
		resp := split(t, client, tc.req)
		if resp.Error == "" || resp.PaneID != "" {
			t.Errorf("%s: resp = %+v, want an error and no pane", name, resp)
			continue
		}
		if tc.want != "" && !strings.Contains(resp.Error, tc.want) {
			t.Errorf("%s: error %q does not mention %q", name, resp.Error, tc.want)
		}
	}
	if got := len(d.buildPaneInfos()); got != before {
		t.Fatalf("pane count %d → %d: a refused split left a pane behind", before, got)
	}
}

// An invalid sandbox must never become a host pane (AC-8).
func TestSplitPaneReq_RefusedSandboxCreatesNothing(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	_, rev := tabTree(t, d, tabID)
	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight,
		Pane: ipc.SplitPaneSpec{Type: "claude-code", Sandbox: &ipc.SandboxSpec{Image: "bad image"}}})
	if resp.Error == "" {
		t.Fatal("an invalid sandbox image was accepted")
	}
	if n := len(d.session.Panes(tabID)); n != 1 {
		t.Fatalf("tab has %d panes, want 1", n)
	}
	if _, after := tabTree(t, d, tabID); after != rev {
		t.Fatalf("layout_rev moved %d → %d on a refusal", rev, after)
	}
}

func TestSplitPaneReq_NewTabOpensATabInTheProject(t *testing.T) {
	d, client := mcpTestDaemon(t)
	proj := d.session.CreateProject("web", t.TempDir())
	resp := split(t, client, ipc.SplitPaneReqPayload{Placement: ipc.PlacementNewTab, NewTab: &ipc.SplitNewTab{Name: "worker", ProjectID: proj.ID}, Pane: ipc.SplitPaneSpec{Name: "w1"}})
	if resp.Error != "" || resp.TabID == "" || resp.PaneID == "" {
		t.Fatalf("resp = %+v", resp)
	}
	if pid, _ := d.session.TabProjectID(resp.TabID); pid != proj.ID {
		t.Fatalf("tab filed under %q, want %q", pid, proj.ID)
	}
}

// One slot per tab: same kind and repo reuses, a different one replaces,
// never two (spec §3.3).
func TestSplitPaneReq_OverlaySlot(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, _ := seedTab(t, d, client)
	repoA, repoB := t.TempDir(), t.TempDir()
	ov := func(kind, repo string) ipc.SplitPaneRespPayload {
		return split(t, client, ipc.SplitPaneReqPayload{TabID: tabID, Placement: ipc.PlacementOverlay, OverlayKind: kind, Pane: ipc.SplitPaneSpec{CWD: repo}})
	}
	a := ov("lazygit", repoA)
	if again := ov("lazygit", repoA); again.PaneID != a.PaneID {
		t.Fatalf("same kind and repo made a new overlay %s, want reuse of %s", again.PaneID, a.PaneID)
	}
	b := ov("lazygit", repoB)
	if b.PaneID == a.PaneID || d.session.Pane(a.PaneID) != nil {
		t.Fatalf("a different repo did not replace the overlay: a=%s b=%s", a.PaneID, b.PaneID)
	}
	c := ov("hunk", repoB)
	if c.PaneID == b.PaneID || d.session.Pane(b.PaneID) != nil {
		t.Fatal("a different kind did not replace the overlay")
	}
	if n := countOverlays(d, tabID); n != 1 {
		t.Fatalf("%d overlays in the tab, want 1", n)
	}
	tree, _ := tabTree(t, d, tabID)
	for _, id := range layouttree.PaneIDs(tree) {
		if id == c.PaneID {
			t.Fatal("the overlay entered the layout tree")
		}
	}
}

// The TUI's own switch (destroy old, then create_pane{overlay}) racing a
// browser create can no longer leave two overlays. The racing sends are raw
// Sends: a goroutine must not call the t.Fatal helpers.
func TestOverlaySlot_TUISwitchRacingABrowserCreate(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	registerShippedPlugins(t, d)
	browser, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { browser.Close() })
	tabID, _ := seedTab(t, d, browser)
	repo := t.TempDir()
	old := split(t, browser, ipc.SplitPaneReqPayload{TabID: tabID, Placement: ipc.PlacementOverlay, OverlayKind: "lazygit", Pane: ipc.SplitPaneSpec{CWD: repo}}).PaneID

	tui, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tui.Close() })
	destroy, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: old})
	create, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tabID, CWD: t.TempDir(), Type: "hunk", Overlay: true})
	race, _ := ipc.NewMessage(ipc.MsgSplitPaneReq, ipc.SplitPaneReqPayload{TabID: tabID, Placement: ipc.PlacementOverlay, OverlayKind: "lazygit", Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}})
	race.ID = "race"
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- tui.Send(destroy)
		errs <- tui.Send(create)
	}()
	go func() {
		defer wg.Done()
		errs <- browser.Send(race)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	// The browser's own answer, so its create has run.
	if err := browser.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := browser.Receive()
		if err != nil {
			t.Fatalf("no answer to the racing split: %v", err)
		}
		if msg.Type == ipc.MsgSplitPaneResp && msg.ID == race.ID {
			break
		}
	}
	// The TUI's sends carry no id; a round trip on its conn orders after them.
	roundTrip(t, tui, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := countOverlays(d, tabID)
		if n == 1 {
			return
		}
		if n > 1 || time.Now().After(deadline) {
			t.Fatalf("%d overlays in the tab, want exactly 1", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Two creates for one session: the second is refused (spec §8).
func TestSplitPaneReq_ResumeClaimRefusesTheSecond(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	prev := transcriptExistsFn
	transcriptExistsFn = func(string) (bool, bool) { return true, true }
	t.Cleanup(func() { transcriptExistsFn = prev })
	const id = "0f3c2a9e-1b2c-4d5e-8f90-1a2b3c4d5e6f"
	req := ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Type: "claude-code", ResumeSessionID: id, CWD: t.TempDir()}}
	one := split(t, client, req)
	if one.PaneID == "" {
		t.Fatalf("first create: %+v", one)
	}
	two := split(t, client, req)
	if two.Error == "" || two.PaneID != "" || !strings.Contains(two.Error, one.PaneID) {
		t.Fatalf("second create for the same session: %+v, want a refusal naming %s", two, one.PaneID)
	}
	if n := len(d.session.Panes(tabID)); n != 2 {
		t.Fatalf("tab has %d panes, want 2", n)
	}
}

// claimResumeSessionID is the atomic half: two goroutines, one winner. Both
// panes are claude-code panes that have not spawned, so each one's pending
// claim is visible to the other.
func TestClaimResumeSessionID_OneWinner(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	a, _ := d.session.CreatePane(tab.ID, t.TempDir())
	b, _ := d.session.CreatePane(tab.ID, t.TempDir())
	for _, p := range []*Pane{a, b} {
		p.PluginMu.Lock()
		p.Type = "claude-code"
		p.PluginMu.Unlock()
	}
	const id = "0f3c2a9e-1b2c-4d5e-8f90-1a2b3c4d5e6f"
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for _, p := range []*Pane{a, b} {
		wg.Add(1)
		go func(p *Pane) { defer wg.Done(); _, ok := d.claimResumeSessionID(p, id); wins <- ok }(p)
	}
	wg.Wait()
	close(wins)
	n := 0
	for ok := range wins {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d claims won, want 1", n)
	}
}

// A spawn held inside newSessionFn holds no lock other clients need.
func TestSplitPaneReq_HeldSpawnDoesNotBlockOtherClients(t *testing.T) {
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	registerShippedPlugins(t, d)
	client, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	_, first := seedTab(t, d, client)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session {
		entered <- struct{}{}
		<-release
		return newLiveFakeSession()
	}
	t.Cleanup(func() { newSessionFn = prev })
	defer close(release)

	m, _ := ipc.NewMessage(ipc.MsgSplitPaneReq, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight})
	m.ID = "held"
	if err := client.Send(m); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the split never reached the spawn")
	}
	other, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	tabs := decodeInto[ipc.ListTabsRespPayload](t, roundTrip(t, other, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{}))
	if len(tabs.Tabs) == 0 {
		t.Fatal("list_tabs answered nothing")
	}
}

// worktreeRepo makes a repository directory and points the worktree listing
// at it, so a branch create resolves its root without git.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "proj", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	prevList := worktreeListFn
	worktreeListFn = func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) {
		return []gitworktree.Worktree{{Path: repo}}, nil
	}
	t.Cleanup(func() { worktreeListFn = prevList })
	return repo
}

// A worktree split answers at once with a placeholder in the slot; the
// finished pane takes the same slot.
func TestSplitPaneReq_WorktreeBranchPlaceholderThenSubstitute(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	repo := worktreeRepo(t)
	gate := make(chan struct{})
	stubAdd(t, func(_ context.Context, _, path, _ string) error { <-gate; return os.MkdirAll(path, 0o755) })

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight,
		Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/x"}}})
	if resp.Error != "" || !resp.Preparing || resp.PaneID == "" {
		close(gate)
		t.Fatalf("resp = %+v", resp)
	}
	placeholder := resp.PaneID
	tree, _ := tabTree(t, d, tabID)
	if tree.Right.PaneID != placeholder {
		close(gate)
		t.Fatalf("placeholder not in the slot: %+v", tree)
	}
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for d.session.Pane(placeholder) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the placeholder was never replaced")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tree, _ = tabTree(t, d, tabID)
	if tree.Left.PaneID != first || tree.Right.PaneID == placeholder || d.session.Pane(tree.Right.PaneID) == nil {
		t.Fatalf("finished pane did not take the placeholder's slot: %+v", tree)
	}
}

// Destroying the placeholder during the checkout leaves no orphan and spawns
// nothing.
func TestSplitPaneReq_PlaceholderClosedDuringCheckout(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	repo := worktreeRepo(t)
	gate := make(chan struct{})
	stubAdd(t, func(_ context.Context, _, path, _ string) error { <-gate; return os.MkdirAll(path, 0o755) })
	var removed []string
	var rmMu sync.Mutex
	prevRm := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, path, _ string) error {
		rmMu.Lock()
		removed = append(removed, path)
		rmMu.Unlock()
		return nil
	}
	t.Cleanup(func() { removeWorktreeFn = prevRm })

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight,
		Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/x"}}})
	roundTrip(t, client, ipc.MsgDestroyPaneReq, ipc.MsgDestroyPaneResp, ipc.DestroyPaneReqPayload{PaneID: resp.PaneID})
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		rmMu.Lock()
		n := len(removed)
		rmMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned worktree was not removed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := d.session.Panes(tabID); len(got) != 1 || got[0].ID != first {
		t.Fatalf("tab panes after a cancelled checkout: %d", len(got))
	}
}

func TestDestroyPaneReq_RemoveWorktreeRemovesTheOwnedCheckout(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")
	wt := filepath.Join(t.TempDir(), "repo-feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	pane, _ := d.session.CreatePane(tab.ID, wt)
	pane.PluginMu.Lock()
	pane.WorktreeOwned, pane.WorktreePath = true, wt
	pane.PluginMu.Unlock()
	called := make(chan []string, 1)
	prev := removeOwnedWorktreesFn
	removeOwnedWorktreesFn = func(_ *Daemon, paths []string) { called <- paths }
	t.Cleanup(func() { removeOwnedWorktreesFn = prev })

	resp := decodeInto[ipc.DestroyPaneRespPayload](t, roundTrip(t, client, ipc.MsgDestroyPaneReq, ipc.MsgDestroyPaneResp,
		ipc.DestroyPaneReqPayload{PaneID: pane.ID, RemoveWorktree: true}))
	if !resp.Success {
		t.Fatal("destroy refused")
	}
	select {
	case paths := <-called:
		if len(paths) != 1 || paths[0] != wt {
			t.Fatalf("removed %v, want [%s]", paths, wt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remove_worktree was ignored")
	}
}

func TestSplitPaneReq_AnswerJSONShape(t *testing.T) {
	raw, _ := json.Marshal(ipc.SplitPaneRespPayload{PaneID: "p", TabID: "t", LayoutRev: 3, Preparing: true})
	if string(raw) != `{"pane_id":"p","tab_id":"t","layout_rev":3,"preparing":true}` {
		t.Fatalf("wire shape %s", raw)
	}
}

// A sandbox the build would refuse is refused before ANYTHING exists, on
// every arm — the worktree arms included, which publish a placeholder and
// start a checkout long before the pane is built.
func TestSplitPaneReq_BadSandboxCreatesNothingOnAnyArm(t *testing.T) {
	d, client := mcpTestDaemon(t)
	_, first := seedTab(t, d, client)
	repo := worktreeRepo(t)
	var addMu sync.Mutex
	adds := 0
	stubAdd(t, func(_ context.Context, _, path, _ string) error {
		addMu.Lock()
		adds++
		addMu.Unlock()
		return os.MkdirAll(path, 0o755)
	})
	proj := d.session.CreateProject("web", repo)
	bad := &ipc.SandboxSpec{Image: "bad image"}
	branch := &ipc.SplitWorktree{Branch: "feat/x"}
	for name, req := range map[string]ipc.SplitPaneReqPayload{
		"worktree split": {TargetPaneID: first, Placement: ipc.PlacementRight,
			Pane: ipc.SplitPaneSpec{Type: "claude-code", CWD: repo, Worktree: branch, Sandbox: bad}},
		"worktree replace": {TargetPaneID: first, Placement: ipc.PlacementReplace,
			Pane: ipc.SplitPaneSpec{Type: "claude-code", CWD: repo, Worktree: branch, Sandbox: bad}},
		"new tab": {Placement: ipc.PlacementNewTab, NewTab: &ipc.SplitNewTab{ProjectID: proj.ID},
			Pane: ipc.SplitPaneSpec{Type: "claude-code", Sandbox: bad}},
		"new tab worktree": {Placement: ipc.PlacementNewTab, NewTab: &ipc.SplitNewTab{ProjectID: proj.ID},
			Pane: ipc.SplitPaneSpec{Type: "claude-code", CWD: repo, Worktree: branch, Sandbox: bad}},
	} {
		tabs, panes := len(d.session.Tabs()), len(d.buildPaneInfos())
		resp := split(t, client, req)
		// The sandbox's own refusal, so an earlier unrelated one cannot pass.
		if !strings.Contains(resp.Error, "invalid container image reference") || resp.PaneID != "" || resp.Preparing {
			t.Errorf("%s: resp = %+v, want the sandbox refusal", name, resp)
		}
		if n := len(d.session.Tabs()); n != tabs {
			t.Errorf("%s: tabs %d → %d", name, tabs, n)
		}
		if n := len(d.buildPaneInfos()); n != panes {
			t.Errorf("%s: panes %d → %d", name, panes, n)
		}
	}
	// One conn dispatches in order, so a round trip proves every refusal's
	// handler has returned; a checkout would have been started by then.
	roundTrip(t, client, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	time.Sleep(100 * time.Millisecond)
	addMu.Lock()
	defer addMu.Unlock()
	if adds != 0 {
		t.Fatalf("git worktree add ran %d times for refused requests", adds)
	}
}

// A new tab's empty cwd is its project root, so the resume check must look
// for the transcript there — not under "".
func TestSplitPaneReq_NewTabResumeLooksInTheProjectRoot(t *testing.T) {
	d, client := mcpTestDaemon(t)
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	proj := d.session.CreateProject("web", root)
	const id = "0f3c2a9e-1b2c-4d5e-8f90-1a2b3c4d5e6f"
	want := map[string]bool{
		claudesessions.TranscriptPath(root, id):     true,
		claudesessions.TranscriptPath(resolved, id): true,
	}
	prev := transcriptExistsFn
	transcriptExistsFn = func(p string) (bool, bool) { return want[p], true }
	t.Cleanup(func() { transcriptExistsFn = prev })

	resp := split(t, client, ipc.SplitPaneReqPayload{Placement: ipc.PlacementNewTab, NewTab: &ipc.SplitNewTab{ProjectID: proj.ID},
		Pane: ipc.SplitPaneSpec{Type: "claude-code", ResumeSessionID: id}})
	if resp.Error != "" || resp.PaneID == "" {
		t.Fatalf("resp = %+v, want the session resumed in the project root", resp)
	}
}

// startFailSession is a PTY whose child never starts.
type startFailSession struct{ fakeSession }

func (s *startFailSession) Start(string, ...string) error {
	return errors.New("spawn refused by the test")
}

// A pane whose child cannot start stays in its slot and says why, and the
// answer carries the same reason.
func TestSplitPaneReq_SpawnFailureCarriesTheError(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return &startFailSession{} }
	t.Cleanup(func() { newSessionFn = prev })

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}})
	if resp.PaneID == "" || !strings.Contains(resp.Error, "spawn refused by the test") {
		t.Fatalf("resp = %+v, want the pane and its spawn error", resp)
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatal("the pane that failed to start is gone")
	}
	if spawnErrorOf(pane) == "" {
		t.Fatal("the pane carries no SpawnError")
	}
	if tree, _ := tabTree(t, d, tabID); tree == nil || tree.Right == nil || tree.Right.PaneID != resp.PaneID {
		t.Fatalf("the failed pane lost its slot: %+v", tree)
	}
}

// A placeholder moved to another tab during the checkout: the add is
// abandoned and removed, no finished pane is built anywhere, and the moved
// placeholder says why.
func TestSplitPaneReq_PlaceholderMovedDuringCheckout(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	other := d.session.CreateTab("other")
	repo := worktreeRepo(t)
	gate := make(chan struct{})
	stubAdd(t, func(_ context.Context, _, path, _ string) error { <-gate; return os.MkdirAll(path, 0o755) })
	removed := make(chan string, 1)
	prevRm := removeWorktreeFn
	removeWorktreeFn = func(_ context.Context, _, path, _ string) error { removed <- path; return nil }
	t.Cleanup(func() { removeWorktreeFn = prevRm })

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight,
		Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/x"}}})
	if !resp.Preparing {
		close(gate)
		t.Fatalf("resp = %+v", resp)
	}
	// The session call, not move_pane: the handler refuses a move while a
	// checkout runs in the tab, and this pins what happens if one lands anyway.
	if _, res := d.session.MovePane(resp.PaneID, other.ID); res != movePaneMoved {
		close(gate)
		t.Fatalf("move: %v", res)
	}
	close(gate)
	select {
	case <-removed:
	case <-time.After(5 * time.Second):
		t.Fatal("the abandoned worktree was not removed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for spawnErrorOf(d.session.Pane(resp.PaneID)) == "" {
		if time.Now().After(deadline) {
			t.Fatal("the moved placeholder never said why its worktree is missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := d.session.Panes(tabID); len(got) != 1 || got[0].ID != first {
		t.Fatalf("source tab holds %d panes, want only %s", len(got), first)
	}
	if got := d.session.Panes(other.ID); len(got) != 1 || got[0].ID != resp.PaneID {
		t.Fatalf("other tab holds %d panes, want only the placeholder", len(got))
	}
}

// A worktree REPLACE has no placeholder; when its add fails after the
// "preparing" answer has gone, the requester hears it in the sidebar, on the
// pane it named, and that pane is untouched.
func TestSplitPaneReq_WorktreeReplaceFailureIsReported(t *testing.T) {
	d, client := mcpTestDaemon(t)
	_, first := seedTab(t, d, client)
	repo := worktreeRepo(t)
	stubAdd(t, func(context.Context, string, string, string) error { return errors.New("branch already exists") })

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementReplace,
		Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/x"}}})
	if !resp.Preparing || resp.PaneID != first {
		t.Fatalf("resp = %+v", resp)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, e := range d.events.Events() {
			if e.Type == "worktree_failed" && e.PaneID == first {
				if !strings.Contains(e.Message, "branch already exists") {
					t.Fatalf("event message %q lacks git's reason", e.Message)
				}
				if d.session.Pane(first) == nil {
					t.Fatal("the target was destroyed by a failed add")
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no worktree_failed event for the replace target")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The answer naming a worktree placeholder reaches the requester before any
// frame the checkout's worker sends: no state frame ahead of the answer may
// already hold the finished pane (create_tab_req and split_pane_req alike).
func TestWorktreeCreates_AnswerPrecedesTheCheckoutsFrames(t *testing.T) {
	d, client := mcpTestDaemon(t)
	_, first := seedTab(t, d, client)
	repo := worktreeRepo(t)
	stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0o755) })
	for i, tc := range []struct {
		msgType, respType string
		payload           any
	}{
		{ipc.MsgCreateTabReq, ipc.MsgCreateTabResp, ipc.CreateTabReqPayload{FirstPane: &ipc.CreatePaneReqPayload{CWD: repo, WorktreeBranch: "feat/a"}}},
		{ipc.MsgSplitPaneReq, ipc.MsgSplitPaneResp, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight,
			Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/b"}}}},
	} {
		branch := []string{"feat/a", "feat/b"}[i]
		wt := gitworktree.DerivePath(repo, branch)
		msg, _ := ipc.NewMessage(tc.msgType, tc.payload)
		msg.ID = "order-" + branch
		if err := client.Send(msg); err != nil {
			t.Fatal(err)
		}
		if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			f, err := client.Receive()
			if err != nil {
				t.Fatalf("%s: no answer: %v", tc.msgType, err)
			}
			if f.Type == tc.respType && f.ID == msg.ID {
				break
			}
			if f.Type != ipc.MsgWorkspaceState {
				continue
			}
			ws := decodeInto[ipc.WorkspaceState](t, f)
			for _, p := range ws.Panes {
				if p.CWD == wt {
					t.Fatalf("%s: a state frame carried the finished pane before the answer", tc.msgType)
				}
			}
		}
		client.SetReadDeadline(time.Time{})
		// Let the checkout finish so the next case's add finds the slot free.
		deadline := time.Now().Add(5 * time.Second)
		for !paneWithCWD(d, wt) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the worktree pane never arrived", tc.msgType)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// The strict claim lost AFTER publish (M1), made deterministic: buildPane is
// called directly, past checkResumeRequest, for a session a live pane holds.
// The published pane is destroyed and a state frame without it goes out — a
// client that saw it in an earlier frame must learn it is gone.
func TestBuildPane_LostStrictClaimIsDestroyedAndBroadcast(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tabID, first := seedTab(t, d, client)
	prev := transcriptExistsFn
	transcriptExistsFn = func(string) (bool, bool) { return true, true }
	t.Cleanup(func() { transcriptExistsFn = prev })
	const id = "0f3c2a9e-1b2c-4d5e-8f90-1a2b3c4d5e6f"
	holder := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: first, Placement: ipc.PlacementRight,
		Pane: ipc.SplitPaneSpec{Type: "claude-code", ResumeSessionID: id, CWD: t.TempDir()}})
	if holder.PaneID == "" {
		t.Fatalf("holder: %+v", holder)
	}
	// Ordered after every frame the holder's create sent.
	roundTrip(t, client, ipc.MsgVersionReq, ipc.MsgVersionResp, struct{}{})
	want := map[string]bool{}
	for _, p := range d.session.Panes(tabID) {
		want[p.ID] = true
	}

	pane, _, err := d.buildPane(ipc.CreatePanePayload{TabID: tabID, Type: "claude-code", ResumeSessionID: id}, t.TempDir(), "claude-code",
		buildOpts{Slot: paneSlot{TabID: tabID, Split: true, TargetID: first, Dir: layouttree.Vertical}, StrictResume: true})
	var taken *errResumeTaken
	if pane != nil || !errors.As(err, &taken) || taken.holder != holder.PaneID {
		t.Fatalf("buildPane = %v, %v; want a refusal naming %s", pane, err, holder.PaneID)
	}
	if n := len(d.session.Panes(tabID)); n != len(want) {
		t.Fatalf("tab holds %d panes, want %d", n, len(want))
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		f, err := client.Receive()
		if err != nil {
			t.Fatalf("no state frame after the lost claim: %v", err)
		}
		if f.Type != ipc.MsgWorkspaceState {
			continue
		}
		ws := decodeInto[ipc.WorkspaceState](t, f)
		got := 0
		for _, p := range ws.Panes {
			if p.TabID != tabID {
				continue
			}
			if !want[p.ID] {
				t.Fatalf("the state frame still carries the destroyed pane %s", p.ID)
			}
			got++
		}
		if got != len(want) {
			t.Fatalf("the state frame carries %d of the tab's %d panes", got, len(want))
		}
		return
	}
}

// A worktree replace whose new pane fails to start after the swap: in a tab
// it left empty, the recovery pane shows the reason and no worktree_failed
// card repeats it; in a tab with other panes nothing shows it, so the card
// is raised.
func TestSplitPaneReq_WorktreeReplaceSpawnFailureIsToldOnce(t *testing.T) {
	d, client := mcpTestDaemon(t)
	lonelyTab, lonely := seedTab(t, d, client)
	_, keep := seedTab(t, d, client)
	crowded := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: keep, Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{CWD: t.TempDir()}}).PaneID
	repo := worktreeRepo(t)
	stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0o755) })
	// A failed create abandons its checkout; nothing here is a real repository.
	prevRm := removeWorktreeFn
	removeWorktreeFn = func(context.Context, string, string, string) error { return nil }
	t.Cleanup(func() { removeWorktreeFn = prevRm })
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return &startFailSession{} }
	t.Cleanup(func() { newSessionFn = prev })
	failedFor := func(paneID string) bool {
		for _, e := range d.events.Events() {
			if e.Type == "worktree_failed" && e.PaneID == paneID {
				return true
			}
		}
		return false
	}

	resp := split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: lonely, Placement: ipc.PlacementReplace,
		Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/a"}}})
	if !resp.Preparing {
		t.Fatalf("lonely: %+v", resp)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		panes := d.session.Panes(lonelyTab)
		if len(panes) == 1 && panes[0].ID != lonely && strings.Contains(spawnErrorOf(panes[0]), "spawn refused by the test") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the emptied tab got no recovery pane carrying the reason")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The second replace queues behind the first's single-flight worktree
	// slot and takes far longer than the first worker's last step, so by the
	// time its card exists the first one would have too.
	resp = split(t, client, ipc.SplitPaneReqPayload{TargetPaneID: crowded, Placement: ipc.PlacementReplace,
		Pane: ipc.SplitPaneSpec{CWD: repo, Worktree: &ipc.SplitWorktree{Branch: "feat/b"}}})
	if !resp.Preparing {
		t.Fatalf("crowded: %+v", resp)
	}
	deadline = time.Now().Add(5 * time.Second)
	for !failedFor(crowded) {
		if time.Now().After(deadline) {
			t.Fatal("a swap that left no pane showing the failure raised no worktree_failed card")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if failedFor(lonely) {
		t.Fatal("worktree_failed repeated a failure the recovery pane already shows")
	}
}

func paneWithCWD(d *Daemon, cwd string) bool {
	for _, p := range d.session.AllPanes() {
		p.PluginMu.Lock()
		got := p.CWD
		p.PluginMu.Unlock()
		if got == cwd {
			return true
		}
	}
	return false
}
