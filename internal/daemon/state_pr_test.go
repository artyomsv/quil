package daemon

import (
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/gitinfo"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/prinfo"
)

// prStateDaemon is one pane in one checkout whose git cache entry the test
// shapes by hand, so buildWorkspaceState's cache-to-wire assignment — not only
// the JSON tags — is what is under test.
func prStateDaemon(t *testing.T, shape func(e *gitEntry)) (*Daemon, string) {
	t.Helper()
	d := newTestDaemonInDir(t, t.TempDir())
	cwd := t.TempDir()
	gitDir := filepath.Join(cwd, ".git")
	e := &gitEntry{info: gitinfo.Info{Branch: "feat/x"}, repo: true, probeDir: cwd}
	shape(e)
	d.gitCache.byDir[gitDir] = e
	d.gitCache.cwdToDir[cwd] = gitDir

	pane := &Pane{ID: "pane-00000001", TabID: "tab-00000001", CWD: cwd}
	tab := &Tab{ID: "tab-00000001", Name: "Build", Panes: []string{pane.ID}, ProjectID: "proj-a"}
	d.session.RestoreTab(tab, []*Pane{pane})
	proj := &Project{ID: "proj-a", Name: "A", RootDir: cwd, TabIDs: []string{tab.ID}, ActiveTab: tab.ID}
	d.session.RestoreProjects([]*Project{proj}, proj.ID)
	return d, pane.ID
}

func paneState(t *testing.T, d *Daemon, id string) ipc.PaneState {
	t.Helper()
	for _, p := range d.buildWorkspaceState().Panes {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("pane %q not in the workspace state", id)
	return ipc.PaneState{}
}

func TestBuildWorkspaceState_PRFieldsFromCache(t *testing.T) {
	d, id := prStateDaemon(t, func(e *gitEntry) {
		e.pr = prinfo.Info{Number: 277, Status: prinfo.StatusApproved}
		e.prKnown, e.prStale = true, true
	})
	p := paneState(t, d, id)
	if p.PRNumber != 277 || p.PRStatus != "approved" || !p.PRStale {
		t.Fatalf("pane PR fields = %d %q stale=%v, want 277 approved stale", p.PRNumber, p.PRStatus, p.PRStale)
	}
	if p.GitBranch != "feat/x" {
		t.Fatalf("git_branch = %q, the branch must still ride beside the PR", p.GitBranch)
	}
}

// An Unknown with nothing known — gh missing, not logged in, a non-GitHub
// remote — sends none of the three keys: the row is exactly today's row.
func TestBuildWorkspaceState_NoPRKnownSendsNothing(t *testing.T) {
	d, id := prStateDaemon(t, func(e *gitEntry) {
		e.prKnown, e.prStale = false, true
	})
	m := stateMap(t, paneState(t, d, id))
	for _, k := range []string{"pr_number", "pr_status", "pr_stale"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q sent with no PR known", k)
		}
	}
}

// The pr_status gate is on PROBING, not on the wire: PR state is runtime-only,
// so after a restart with the feature off there is nothing to send, and a
// value already held is still the truth about the checkout.
func TestBuildWorkspaceState_FeatureOffStillSendsAHeldAnswer(t *testing.T) {
	d, id := prStateDaemon(t, func(e *gitEntry) {
		e.pr = prinfo.Info{Number: 5, Status: prinfo.StatusOpen}
		e.prKnown = true
	})
	d.gitCache.prEnabled = false
	if p := paneState(t, d, id); p.PRNumber != 5 {
		t.Fatalf("pr_number = %d, want the held 5", p.PRNumber)
	}
}
