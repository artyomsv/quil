package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/kubediscover"
)

// mkRepo creates a directory that gitdiscover will recognise as a repository.
func mkRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	return dir
}

func TestGitReposResponse_FindsTheEnclosingRepo(t *testing.T) {
	root := mkRepo(t, t.TempDir())

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: root}, "")

	if got.Error != "" {
		t.Fatalf("Error = %q, want empty", got.Error)
	}
	if len(got.Repos) == 0 {
		t.Fatal("no repos found for a directory that is itself a repo")
	}
	// EvalSymlinks is applied inside gitdiscover, so compare resolved forms.
	want, _ := filepath.EvalSymlinks(root)
	if got.Repos[0] != want && got.Repos[0] != root {
		t.Errorf("Repos[0] = %q, want %q", got.Repos[0], want)
	}
}

// The echo is the staleness key: the answer only means anything for the
// directory that was asked about, and the user may have moved on.
func TestGitReposResponse_EchoesCWDVerbatim(t *testing.T) {
	root := mkRepo(t, t.TempDir())
	req := ipc.GitReposReqPayload{CWD: root + string(filepath.Separator)}

	got := gitReposResponse(req, "")

	if got.CWD != req.CWD {
		t.Errorf("CWD = %q, want %q — normalising here makes a live request look stale",
			got.CWD, req.CWD)
	}
}

// TestGitReposResponse_NoRepoIsAnAnswerNotAnError pins the distinction the UI
// depends on.
//
// "No repository here" is a finding and the TUI flashes it. A failure is not,
// and must not be reported as one — that is precisely the wrong-but-confident
// answer this phase exists to remove.
func TestGitReposResponse_NoRepoIsAnAnswerNotAnError(t *testing.T) {
	plain := t.TempDir()

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: plain}, "")

	if got.Error != "" {
		t.Errorf("Error = %q, want empty — an absent repo is a real answer", got.Error)
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none", got.Repos)
	}
}

// A missing directory must not be reported as "no repositories" either.
func TestGitReposResponse_MissingDirEchoesAndFindsNothing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: missing}, "")

	if got.CWD != missing {
		t.Errorf("CWD = %q, want the request echoed", got.CWD)
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none for a directory that does not exist", got.Repos)
	}
}

func TestGitReposResponse_EmptyCWDUsesTheFallback(t *testing.T) {
	root := mkRepo(t, t.TempDir())

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: ""}, root)

	if got.CWD != "" {
		t.Errorf("CWD = %q, want the empty request echoed unchanged", got.CWD)
	}
	if len(got.Repos) == 0 {
		t.Error("the fallback directory was not scanned")
	}
}

// A rejected scan must still echo the CWD, or the TUI drops it as stale and
// waits out its whole timeout on an answer it already holds.
func TestBeginGitDiscover_RejectionEchoesTheCWD(t *testing.T) {
	d := &Daemon{}
	msg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: "/srv/work"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}

	if _, ok := d.beginGitDiscover(msg); !ok {
		t.Fatal("first claim was refused; the slot starts free")
	}
	rejection, ok := d.beginGitDiscover(msg)
	if ok {
		t.Fatal("second claim succeeded; the single-flight guard does not hold")
	}
	if rejection.CWD != "/srv/work" {
		t.Errorf("rejection CWD = %q, want the request echoed", rejection.CWD)
	}
	if rejection.Error == "" {
		t.Error("rejection carries no reason")
	}

	d.gitDiscovering.Store(false)
	if _, ok := d.beginGitDiscover(msg); !ok {
		t.Error("slot was not released")
	}
}

// The browse and discovery guards must be independent: the setup dialog
// resolves a directory and then scans it, so a shared slot would make each step
// fail exactly when it followed the other.
func TestGitDiscoverAndBrowse_HaveIndependentSlots(t *testing.T) {
	d := &Daemon{}
	gitMsg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: "/a"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if _, ok := d.beginGitDiscover(gitMsg); !ok {
		t.Fatal("git slot refused while free")
	}
	if _, ok := d.beginBrowseScan(ipc.BrowseDirReqPayload{Path: "/a"}); !ok {
		t.Error("a held git-discovery slot blocked a directory listing; the guards are shared")
	}
}

// The parallel of TestBrowseDirResponse_NoPathAndNoFallback: an empty request
// with no fallback is an answer, and it must be distinguishable from the
// genuine finding "no repositories here" — only one of the two is news.
func TestGitReposResponse_NoCWDAndNoFallback(t *testing.T) {
	got := gitReposResponse(ipc.GitReposReqPayload{}, "")

	if got.Error == "" {
		t.Error("Error is empty when there is nothing to scan and no default; " +
			"the caller cannot tell this from 'no repository here'")
	}
	if len(got.Repos) != 0 {
		t.Errorf("Repos = %v, want none", got.Repos)
	}
}

// TestDiscoverWithin_TimerFiresOnAHungWalk pins that the CALL is bounded, not
// just the walk.
//
// gitdiscover.Candidates takes a context and checks it between steps, which
// reads like a bound but is not one: a context cannot interrupt an os.Stat or
// os.ReadDir already blocked in the kernel, and an unresponsive mount is exactly
// that. The call holds the single-flight slot, so an unbounded one answers every
// later Alt+G in the session with "another repository scan is already running".
func TestDiscoverWithin_TimerFiresOnAHungWalk(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	orig := discoverCandidates
	discoverCandidates = func(context.Context, string) []string {
		<-block
		return nil
	}
	t.Cleanup(func() { restoreSeam(t, block, func() { discoverCandidates = orig }) })

	start := time.Now()
	repos, answered := discoverWithin("/never-answers", 50*time.Millisecond)
	elapsed := time.Since(start)

	if answered {
		t.Error("a walk that never returns reported as answered")
	}
	if repos != nil {
		t.Errorf("repos = %v, want nil when the walk never answered", repos)
	}
	if elapsed > time.Second {
		t.Errorf("took %v against a 50ms budget; the scan is not bounded", elapsed)
	}
}

// The timeout must reach the user as an error, not as the finding "no repos".
func TestGitReposResponse_HungWalkReportsTimeoutNotEmpty(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	origDiscover, origTimeout := discoverCandidates, gitDiscoverTimeout
	discoverCandidates = func(context.Context, string) []string {
		<-block
		return nil
	}
	gitDiscoverTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		restoreSeam(t, block, func() {
			discoverCandidates, gitDiscoverTimeout = origDiscover, origTimeout
		})
	})

	got := gitReposResponse(ipc.GitReposReqPayload{CWD: "/srv/work"}, "")

	if got.Error == "" {
		t.Error("a timed-out scan reported as 'no repositories here'; only one of " +
			"those two is a finding")
	}
	if got.CWD != "/srv/work" {
		t.Errorf("CWD = %q, want the request echoed even on the timeout path", got.CWD)
	}
}

// TestGitDiscover_SlotIsReleasedWhenTheWalkHangs pins that a hung walk does NOT
// hold the single-flight slot.
//
// Raised in review as still-broken after b8a7f0f: the claim was that
// gitDiscovering is released only when the abandoned worker exits, so one dead
// mount disables every later scan until the daemon restarts. That is not what
// the code does, and the distinction is the whole point of the fix — TWO
// goroutines are involved. The one holding gitDiscovering runs gitReposResponse
// and returns when discoverWithin hits its deadline; the one parked in the walk
// holds only a blockingFSCalls slot and owns nothing the dialog depends on.
//
// Mirrors handleGitReposReq's structure rather than calling it, because ipc.Conn
// cannot be constructed outside its own package — the same documented limit that
// put beginGitDiscover in its own function.
func TestGitDiscover_SlotIsReleasedWhenTheWalkHangs(t *testing.T) {
	waitForFSCallsDrained(t)

	block := make(chan struct{})
	origDiscover, origTimeout := discoverCandidates, gitDiscoverTimeout
	discoverCandidates = func(context.Context, string) []string {
		<-block
		return nil
	}
	gitDiscoverTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		restoreSeam(t, block, func() {
			discoverCandidates, gitDiscoverTimeout = origDiscover, origTimeout
		})
	})

	d := &Daemon{}
	msg, err := ipc.NewMessage(ipc.MsgGitReposReq, ipc.GitReposReqPayload{CWD: "/srv/work"})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if _, ok := d.beginGitDiscover(msg); !ok {
		t.Fatal("slot refused while free")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Exactly handleGitReposReq's defer. Registered second, so it runs
		// before close(done): by the time this test observes done, the slot
		// release has already happened.
		defer d.gitDiscovering.Store(false)
		gitReposResponse(gitReposReq(msg), "")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never returned; the hung walk is holding the slot")
	}

	if _, ok := d.beginGitDiscover(msg); !ok {
		t.Error("the slot is still held after the walk timed out; every later scan " +
			"would be rejected until the daemon restarts")
	}
}

func TestKubeCtxResponse_ReportsContextsWithCurrentMarked(t *testing.T) {
	restore := kubeContexts
	t.Cleanup(func() { kubeContexts = restore })
	kubeContexts = func(context.Context) []kubediscover.Context {
		return []kubediscover.Context{
			{Name: "ctx-a", Namespace: "ns-a"},
			{Name: "ctx-b", Current: true},
		}
	}

	out := kubeCtxResponse()
	if out.Error != "" {
		t.Fatalf("Error = %q, want none", out.Error)
	}
	if len(out.Contexts) != 2 {
		t.Fatalf("Contexts = %d, want 2", len(out.Contexts))
	}
	if out.Contexts[0].Name != "ctx-a" || out.Contexts[0].Namespace != "ns-a" {
		t.Errorf("Contexts[0] = %+v, want name ctx-a namespace ns-a", out.Contexts[0])
	}
	if !out.Contexts[1].Current {
		t.Error("Contexts[1].Current = false — the current-context marker did not survive the conversion")
	}
}

// The cap is enforced daemon-side so a hostile or misconfigured kubeconfig
// cannot hand the TUI an unbounded list to render.
func TestKubeCtxResponse_CapsAtMaxKubeContexts(t *testing.T) {
	restore := kubeContexts
	t.Cleanup(func() { kubeContexts = restore })
	kubeContexts = func(context.Context) []kubediscover.Context {
		out := make([]kubediscover.Context, maxKubeContexts+5)
		for i := range out {
			out[i] = kubediscover.Context{Name: fmt.Sprintf("ctx-%d", i)}
		}
		return out
	}

	out := kubeCtxResponse()
	if len(out.Contexts) != maxKubeContexts {
		t.Errorf("Contexts = %d, want capped to %d", len(out.Contexts), maxKubeContexts)
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true — a silently short list reads as the whole list")
	}
}

// An absent kubeconfig is a real finding ("no contexts"), NOT a failure. The
// two render differently and only one of them is worth a diagnostic.
func TestKubeCtxResponse_NoContextsIsNotAnError(t *testing.T) {
	restore := kubeContexts
	t.Cleanup(func() { kubeContexts = restore })
	kubeContexts = func(context.Context) []kubediscover.Context { return nil }

	out := kubeCtxResponse()
	if out.Error != "" {
		t.Errorf("Error = %q, want none — an empty kubeconfig is not a failure", out.Error)
	}
	if len(out.Contexts) != 0 {
		t.Errorf("Contexts = %d, want 0", len(out.Contexts))
	}
}

// The refutation test for the bound: a parse that never returns must not hold
// the single-flight slot, and must be reported as a timeout rather than as
// "no contexts". Mirrors TestGitReposResponse_HungWalkReportsTimeoutNotEmpty.
func TestKubeCtxResponse_HungReadReportsTimeoutNotEmpty(t *testing.T) {
	restoreFn, restoreTimeout := kubeContexts, kubeDiscoverTimeout
	t.Cleanup(func() { kubeContexts, kubeDiscoverTimeout = restoreFn, restoreTimeout })

	unpark := make(chan struct{})
	kubeContexts = func(context.Context) []kubediscover.Context {
		<-unpark
		return nil
	}
	kubeDiscoverTimeout = 50 * time.Millisecond

	done := make(chan ipc.KubeCtxRespPayload, 1)
	go func() { done <- kubeCtxResponse() }()

	select {
	case out := <-done:
		if out.Error == "" {
			t.Error("Error = empty — a timed-out scan reported as 'no contexts' is a wrong answer stated confidently")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("kubeCtxResponse never returned — the wall-clock bound is not in force")
	}

	// Release the parked reader and wait for the slot before the seam is
	// restored: restoring it while a goroutine is still inside is a data race.
	close(unpark)
	waitForFSCallsDrained(t)
}
